package handlers

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/ovh-buy/server/internal/app"
	"github.com/ovh-buy/server/internal/netfp"
	"github.com/ovh-buy/server/internal/proxyguard"
)

// TestAccountProxy POST /api/accounts/:id/proxy-test
//
// 用这个账户**当前生效的出站配置**查一次真实出口 IP。
//
// 为什么必须有这个：隔离有没有生效，光看配置界面是看不出来的。
// 代理写错了、或者代理本身把流量透明转发回本机，界面上都一模一样。
// 唯一可靠的确认方式是：两个账户查出来的出口 IP 不同。
//
// 走的是和业务请求完全相同的 transport（同样的代理、同样的指纹），
// 所以查出来的就是真正下单时用的那个出口。
func TestAccountProxy(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		acc, ok := state.FindAccount(id)
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "账户不存在"})
			return
		}
		prof, warn := netfp.LookupProfile(acc.Fingerprint)
		ip, err := netfp.EgressIP(netfp.Options{ProxyURL: acc.ProxyURL, Profile: prof})
		if err != nil {
			// 失败必须说清楚是走代理失败还是直连失败 —— 两者的处理完全不同
			via := "直连"
			if strings.TrimSpace(acc.ProxyURL) != "" {
				via = "代理 " + netfp.ScrubProxyURL(acc.ProxyURL)
			}
			c.JSON(http.StatusOK, gin.H{
				"success":     false,
				"error":       err.Error(),
				"via":         via,
				"usingProxy":  strings.TrimSpace(acc.ProxyURL) != "",
				"fingerprint": prof.Name,
			})
			return
		}
		out := gin.H{
			"success":     true,
			"egressIP":    ip,
			"usingProxy":  strings.TrimSpace(acc.ProxyURL) != "",
			"proxy":       netfp.ScrubProxyURL(acc.ProxyURL),
			"fingerprint": prof.Name,
		}
		if warn != "" {
			out["warning"] = warn
		}
		c.JSON(http.StatusOK, out)
	}
}

// ProxyStatus GET /api/accounts/proxy-status
//
// 各账户的代理健康状况：连续失败几次、有没有因为代理故障被停。
// 被停的账户在界面上必须看得见 —— 否则用户只会觉得"这个账户一直抢不到"。
func ProxyStatus(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		guard := proxyguard.Status()
		accs := listAccounts(state)
		out := make([]gin.H, 0, len(accs))
		for _, a := range accs {
			e := gin.H{
				"id":          a.ID,
				"name":        a.Name,
				"zone":        strings.ToUpper(a.Zone),
				"usingProxy":  strings.TrimSpace(a.ProxyURL) != "",
				"proxy":       netfp.ScrubProxyURL(a.ProxyURL),
				"fingerprint": a.Fingerprint,
				"tripped":     false,
				"fails":       0,
			}
			if g, ok := guard[a.ID]; ok {
				e["tripped"] = g["tripped"]
				e["fails"] = g["fails"]
				if v, ok := g["trippedAt"]; ok {
					e["trippedAt"] = v
				}
				if v, ok := g["lastFailAt"]; ok {
					e["lastFailAt"] = v
				}
			}
			out = append(out, e)
		}
		c.JSON(http.StatusOK, gin.H{
			"success":  true,
			"accounts": out,
			"profiles": netfp.ProfileNames(),
		})
	}
}

// RefreshSharedProxy 账户变动后重设"公开请求"的统一出口。
//
// 默认账户可能换了、或者它的代理改了 —— 不重设的话，
// 公开目录仍然从旧出口（甚至本机真实 IP）发出去。
func RefreshSharedProxy(state *app.State) {
	acc, ok := state.FindAccount("")
	if !ok {
		_ = netfp.SetSharedProxy("")
		return
	}
	if err := netfp.SetSharedProxy(acc.ProxyURL); err != nil {
		state.Logger.Warn("公开请求的统一出口设置失败,将走直连: "+err.Error(), "proxy")
	}
}
