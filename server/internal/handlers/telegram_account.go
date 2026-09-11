package handlers

import (
	"fmt"
	"strings"

	"github.com/ovh-buy/server/internal/app"
	"github.com/ovh-buy/server/internal/monitor"
	"github.com/ovh-buy/server/internal/telegram"
	"github.com/ovh-buy/server/internal/types"
)

// 下单时该用哪个账户 —— 算出来，而不是让用户记着。
//
// 以前是"全局当前账户"那一套：切一次，之后所有操作跟着走。问题在于它是**模态**的：
// 三天前切到美区，今天敲 `24sk602 gra 2`（欧区机型），它就闷头落到美区。
// 而你在敲那条命令的那一刻，界面上根本看不见当前账户是谁。
// 更要命的是这种错不会报错：拿欧区 planCode 打美区接口，OVH 回 200 + 空数组，
// 表现就是"永远抢不到"，日志里也没有异常。
//
// 现在的顺序：
//  1. 用 planCode 反推大区（monitor.PlanAccount，带缓存 + 目录快捷判定 + 可用性探测兜底）
//  2. 当前账户如果正好在对的大区，优先用它 —— 这是给"同区多账户"当决胜局的
//  3. 算不出来（三区都没这个机型 / 没配对应大区的账户）→ 明确说清楚，不猜

// resolvedAccount 一次账户解析的结果。
type resolvedAccount struct {
	Account types.OVHAccount
	Region  string
	// Confident 是不是真的按 planCode 算出来的。
	// false = 退回了当前/默认账户，界面上必须说出来。
	Confident bool
	// Reason 一个账户都没有时的说明。
	Reason string
	// Ambiguous 同一个大区里有多个账户，需要用户挑一次。
	Ambiguous []types.OVHAccount
}

// resolveOrderAccount 决定这个 planCode 该落到哪个账户。
//
// 原则：**能算就算，算不出来也绝不拒绝**。
//
// 判不出来的常见原因是目录还没热，而不是真的没有合适的账户 ——
// 为这个拒绝挂单，等于让一次网络抖动毁掉一次补货。
// 所以判不出来时退回当前账户，但要把"没能确认"这件事写在回复里：
// 用户是唯一知道自己想买哪个区的人。
//
// 另外这里只查缓存目录、不做跨区探测：探测要逐个大区打 OVH，实测好几秒，
// 而补货那一刻每一秒都算数。真正的区域错配由监控每轮检查时坐实
// （LastCheckError → /subs 里能看到）。
func resolveOrderAccount(state *app.State, mon *monitor.Monitor, planCode string) resolvedAccount {
	if len(listAccounts(state)) == 0 {
		return resolvedAccount{Reason: "还没有配置 OVH 账户。请到控制台「设置 → OVH 账户」添加。"}
	}
	cur, _ := telegram.ActiveAccount(state)
	fallback := resolvedAccount{Account: cur, Region: strings.ToUpper(strings.TrimSpace(cur.Zone))}

	if mon == nil {
		return fallback
	}
	accID, region, _ := mon.PlanAccountFast(planCode, cur.ID)
	if accID == "" {
		return fallback
	}
	acc, ok := state.FindAccount(accID)
	if !ok {
		return fallback
	}

	// 同区多账户：光看 planCode 算不出来用哪个，让用户挑一次。
	// 当前账户已经在这个区时不问 —— 上面 prefer 已经选中它了。
	if inRegion := mon.AccountsInRegion(region); len(inRegion) > 1 && cur.ID != acc.ID {
		return resolvedAccount{Account: acc, Region: region, Confident: true, Ambiguous: inRegion}
	}
	return resolvedAccount{Account: acc, Region: region, Confident: true}
}

// explainAccountChoice 一句话说明这单为什么落在这个账户上。
//
// 必须说：账户选错的后果是"永远抢不到"且 OVH 不报错，
// 用户唯一能发现的机会就是下单那一刻看到它落在哪儿了。
//
// 尤其是"没能确认"那一支 —— 那正是最可能出错的情况，更要说。
func explainAccountChoice(r resolvedAccount, planCode string) string {
	if r.Account.ID == "" {
		return ""
	}
	if r.Confident {
		return fmt.Sprintf("账户：%s\n（%s 在这个账户的目录里，已自动选定）",
			telegram.AccountLabel(r.Account), planCode)
	}
	return fmt.Sprintf("账户：%s\n"+
		"⚠️ 没能确认 %s 属于哪个区，用的是当前账户。\n"+
		"   OVH 三个站点互不相通，用错区的账户下单不会报错，只会一直抢不到。\n"+
		"   要换账户发 /accounts。",
		telegram.AccountLabel(r.Account), planCode)
}

// askOrderAccount 同一大区有多个账户时，让用户挑一次。
//
// 挑完会记成当前账户，所以这个问题在同一个区里只会问一次；
// 换到别的区的机型时又会自动解析，不需要再切回来。
func askOrderAccount(state *app.State, chatID interface{}, messageID int64,
	info *telegram.OrderInfo, ra resolvedAccount) {

	f := &watchFlow{
		Step:     stepOrderAccount,
		Accounts: ra.Ambiguous,
		PlanCode: info.PlanCode,
		Order:    info,
	}
	tok := putFlow(f)

	labels := make([]string, 0, len(ra.Ambiguous))
	for _, a := range ra.Ambiguous {
		labels = append(labels, telegram.AccountLabel(a))
	}
	telegram.SendKeyboard(state, chatID, messageID, fmt.Sprintf(
		"👤 %s 属于 %s 区，而你在这个区有 %d 个账户 —— 用哪个下单？\n\n"+
			"（选完会记住，同一个区之后不再问；换别的区的机型会自动切）",
		info.PlanCode, ra.Region, len(ra.Ambiguous)), flowKeyboard(tok, labels))
}
