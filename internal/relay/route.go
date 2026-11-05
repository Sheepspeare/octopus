package relay

import (
	"maps"
	"sync"
	"time"

	"github.com/bestruirui/octopus/internal/model"
)

// RouteState 是一个分组的进程内路由状态; 跨该分组的全部请求共享。
// 同时作为路由流的消息形状与分组读取响应中的 runtime 字段: 冷却, 探测与亲和都是本包路由算法的概念,
// 故状态形状由本包定义, 分组的持久化配置不含它; 内部标志未导出, 不会随消息出到 JSON。
// 两种模式共用 CurrentItemID: 手动模式下即人工指定的成员, 故障转移模式下由路由决定,
// 前端由此只读这一个字段即可知道当前承载请求的成员, 无需再按模式分支。
type RouteState struct {
	GroupID       int           `json:"group_id"`        // 状态所属的分组 ID, 供状态流按分组定位。
	CurrentItemID int           `json:"current_item_id"` // 当前承载请求的成员 ID, 0 表示尚未建立路由或未人工指定。
	ProbeItemID   int           `json:"probe_item_id"`   // 当前占用恢复探测的成员 ID, 同一分组同时只允许一个成员被探测; 手动模式恒为 0。
	AffinityUntil int64         `json:"affinity_until"`  // 当前路由的亲和截止 Unix 毫秒时间, 0 表示无亲和; 手动模式恒为 0。
	Cooldowns     map[int]int64 `json:"cooldowns"`       // 失败成员 ID 对应的冷却截止 Unix 毫秒时间, 已到期的条目由前端按当前时间忽略。

	affinityArmed bool // 当前路由下一次成功后是否开始亲和, 仅故障切换后为真。
	// inflight 记录每个成员当前在途的请求数, 用于执行成员的并发上限; 未配置上限的成员同样计数, 便于排障。
	// 只在持有 routeMu 时读写, 不导出到 JSON: 前端展示的实时状态不含在途数。
	inflight map[int]int
	// weights 是加权轮询的平滑累计权重: 每轮给候选成员加上自身权重, 被选中的成员减去本轮总权重。
	// 这样既保持长期比例精确, 又不会像固定窗口那样把同一成员的请求挤在一起。
	weights map[int]int
	// provisional 标记 Cooldowns 中由确定性 4xx 临时压下的条目。
	// 这类失败可能是请求本身有问题, 不能立刻算作成员故障; 但同批并发请求都在看着同一份冷却表,
	// 若不立刻压下, 一个坏成员会在几秒内被每个并发请求各撞一次。故先压一个短冷却,
	// 等本次请求在别的成员上成功再升级为正式冷却, 若整个请求都被同类 4xx 拒绝则整条撤销。
	provisional map[int]bool
}

const routeStreamBuffer = 16 // 单个路由流连接的非阻塞消息缓冲容量。

var (
	routeMu      sync.Mutex                           // routeMu 保护全部分组路由状态。
	routes       = make(map[int]*RouteState)          // routes 按分组 ID 保存路由状态。
	routeStreams = make(map[chan RouteState]struct{}) // 全部路由 SSE 连接。
)

// RouteStateOf 返回分组当前的实时路由状态, 供读取接口随分组一并返回。
// 手动模式没有进程内路由: 当前成员即人工指定的成员, 冷却与亲和均不适用, 故直接由分组配置得出。
func RouteStateOf(group model.Group) RouteState {
	if group.Mode == model.GroupModeManual {
		return RouteState{
			GroupID:       group.ID,
			CurrentItemID: group.ActiveItemID,
			Cooldowns:     map[int]int64{},
		}
	}

	routeMu.Lock()
	defer routeMu.Unlock()

	route := routes[group.ID]
	if route == nil {
		return RouteState{GroupID: group.ID, Cooldowns: map[int]int64{}}
	}
	state := *route
	state.Cooldowns = maps.Clone(route.Cooldowns)
	return state
}

// ResetRouteState 丢弃分组的进程内路由状态, 用于分组切换选择模式或被删除。
// 不丢弃的话冷却与亲和会在 failover 切到 manual 再切回来之后复活并继续影响选路, 分组删除后其状态也会永久残留。
func ResetRouteState(groupID int) {
	routeMu.Lock()
	defer routeMu.Unlock()

	delete(routes, groupID)
}

// pickGroupItem 按分组模式选择本轮目标成员, 没有可选成员时返回零值; group.Items 已按 Priority 升序排列。
// 渠道是否可用不在此判断: GroupGetByName 已把渠道或凭据被禁用的成员滤掉, 这里只看路由状态。
// exclude 是本请求内已经确认不值得再试的成员(例如上游对同一请求返回确定性的 4xx),
// 排除只作用于当前请求: 请求结束即失效, 是否真正冷却由调用方在确认成员侧故障后决定。
// 自动模式返回非零成员时已为其占用一个并发槽位, 调用方必须在该轮结束后调用 ReleaseItemSlot 归还;
// 成员并发上限只在自动模式生效: 手动模式是人工指定"就用它", 不在此处限流。
func pickGroupItem(group model.Group, exclude map[int]bool) model.GroupItem {
	if group.Mode == model.GroupModeManual {
		for _, item := range group.Items {
			if item.ID == group.ActiveItemID {
				return item
			}
		}
		return model.GroupItem{}
	}

	routeMu.Lock()
	defer routeMu.Unlock()

	route := groupRouteLocked(group)
	now := time.Now().UnixMilli()

	// 轮询与加权模式不看亲和: 分摊是否均匀由轮转或权重本身保证。
	switch group.Mode {
	case model.GroupModeRoundRobin:
		return pickRoundRobinLocked(group, route, now, exclude)
	case model.GroupModeWeighted:
		return pickWeightedLocked(group, route, now, exclude)
	}

	if route.AffinityUntil <= now {
		route.AffinityUntil = 0
	}

	// 亲和期内沿用当前成员, 不提前探测已恢复的高优先级成员; 当前成员被排除或满员时放弃亲和。
	if route.CurrentItemID != 0 && route.AffinityUntil > now && !exclude[route.CurrentItemID] {
		if item := itemOf(group, route.CurrentItemID); item.ID != 0 && itemHasSlot(route, item) {
			reserveItemLocked(route, item)
			return item
		}
	}

	for _, item := range group.Items {
		// 被排除的成员等同不可选: 当前成员若在其中, 不能在这里 break, 否则会把一个已知失败的成员继续交回去。
		if exclude[item.ID] {
			continue
		}
		// 遍历到当前成员说明比它优先级更高的成员都不可选: 它仍有余量就沿用, 满员则继续向后找承接者,
		// 否则整个分组会在一个满员的成员上排队, 而后面明明还有空闲成员。
		if item.ID == route.CurrentItemID {
			if itemHasSlot(route, item) {
				reserveItemLocked(route, item)
				return item
			}
			continue
		}
		deadline, cooling := route.Cooldowns[item.ID]
		if cooling && deadline > now {
			continue
		}
		if !itemHasSlot(route, item) {
			continue
		}
		// 冷却已到期的成员只放行一个探测请求, 避免全部请求同时涌向尚未恢复的成员。
		if cooling {
			if route.ProbeItemID != 0 {
				continue
			}
			route.ProbeItemID = item.ID
			reserveItemLocked(route, item)
			publishRouteLocked(route)
			return item
		}
		route.CurrentItemID = item.ID
		reserveItemLocked(route, item)
		publishRouteLocked(route)
		return item
	}
	// 全部成员都在冷却或满员: 返回零值交给调用方等待, 比对着已知不可用的成员空转更省资源。
	if item := itemOf(group, route.CurrentItemID); item.ID != 0 && !exclude[item.ID] && itemHasSlot(route, item) {
		reserveItemLocked(route, item)
		return item
	}
	return model.GroupItem{}
}

// pickRoundRobinLocked 从当前成员的下一个开始轮转, 跳过冷却中与本请求已排除的成员, 使请求按成员顺序均匀分摊。
// 全部成员都不可选时返回零值: 交给调用方按重试间隔等待, 比对着已知失败的成员空转更省资源,
// 冷却到期后成员会自动回到轮转序列。
// 调用方必须持有 routeMu。
func pickRoundRobinLocked(group model.Group, route *RouteState, now int64, exclude map[int]bool) model.GroupItem {
	if len(group.Items) == 0 {
		return model.GroupItem{}
	}
	start := 0
	for index, item := range group.Items {
		if item.ID == route.CurrentItemID {
			start = index + 1
			break
		}
	}
	for offset := range group.Items {
		item := group.Items[(start+offset)%len(group.Items)]
		if exclude[item.ID] {
			continue
		}
		if deadline, cooling := route.Cooldowns[item.ID]; cooling && deadline > now {
			continue
		}
		// 满员的成员本轮让位: 游标继续推进, 它的份额临时由后面的成员承接。
		if !itemHasSlot(route, item) {
			continue
		}
		route.CurrentItemID = item.ID
		reserveItemLocked(route, item)
		publishRouteLocked(route)
		return item
	}
	return model.GroupItem{}
}

// pickWeightedLocked 按成员权重做平滑加权轮询, 权重来自成员的并发上限。
// 每轮给全部候选成员累加自身权重, 取累计值最大者并减去本轮总权重: 长期比例精确, 短期也不会把同一成员连发。
// 满员与冷却中的成员不参与本轮累计, 它们的份额自然落到仍有余量的成员身上; 全部满员时返回零值交给调用方等待。
// 调用方必须持有 routeMu。
func pickWeightedLocked(group model.Group, route *RouteState, now int64, exclude map[int]bool) model.GroupItem {
	total := 0
	chosen := model.GroupItem{}
	for _, item := range group.Items {
		if exclude[item.ID] {
			continue
		}
		if deadline, cooling := route.Cooldowns[item.ID]; cooling && deadline > now {
			continue
		}
		if !itemHasSlot(route, item) {
			continue
		}
		weight := itemWeight(group, item)
		route.weights[item.ID] += weight
		total += weight
		if chosen.ID == 0 || route.weights[item.ID] > route.weights[chosen.ID] {
			chosen = item
		}
	}
	if chosen.ID == 0 || total == 0 {
		return model.GroupItem{}
	}
	route.weights[chosen.ID] -= total
	route.CurrentItemID = chosen.ID
	reserveItemLocked(route, chosen)
	publishRouteLocked(route)
	return chosen
}

// itemWeight 返回成员在加权轮询中的权重: 配了并发上限就用上限本身, 没配则取组内最大上限,
// 使"不限并发"的成员与容量最大的成员同权, 由并发闸门决定它实际能承接多少; 全组都没配上限时各成员等权。
func itemWeight(group model.Group, item model.GroupItem) int {
	if item.MaxConcurrency > 0 {
		return item.MaxConcurrency
	}
	maxLimit := 0
	for _, other := range group.Items {
		if other.MaxConcurrency > maxLimit {
			maxLimit = other.MaxConcurrency
		}
	}
	if maxLimit > 0 {
		return maxLimit
	}
	return 1
}

// itemHasSlot 判断成员是否还有并发余量; 未配置上限的成员恒为真。
func itemHasSlot(route *RouteState, item model.GroupItem) bool {
	if item.MaxConcurrency <= 0 {
		return true
	}
	return route.inflight[item.ID] < item.MaxConcurrency
}

// reserveItemLocked 为已通过 itemHasSlot 的成员占用一个并发槽位。
// 占位与选路在同一把锁内完成, 避免同批并发请求同时看到"最后一个空位"而超发。
func reserveItemLocked(route *RouteState, item model.GroupItem) {
	route.inflight[item.ID]++
}

// ReleaseItemSlot 归还成员的一个并发槽位, 供 Relay 在每轮结束和请求退出时调用。
// 重复调用或对未占用的成员调用都是安全的: 计数不会降到零以下, 也不会影响冷却与亲和。
func ReleaseItemSlot(groupID, itemID int) {
	if itemID == 0 {
		return
	}

	routeMu.Lock()
	defer routeMu.Unlock()

	route := routes[groupID]
	if route == nil {
		return
	}
	switch inflight := route.inflight[itemID]; {
	case inflight > 1:
		route.inflight[itemID] = inflight - 1
	case inflight == 1:
		delete(route.inflight, itemID)
	}
}

// allItemsExcluded 判断分组内每个成员是否都已在本次请求中被排除。
// 用于识别"问题在请求本身": 全部成员都对同一请求给出确定性拒绝时, 再换成员也不会有不同结果。
func allItemsExcluded(group model.Group, exclude map[int]bool) bool {
	if len(group.Items) == 0 {
		return false
	}
	for _, item := range group.Items {
		if !exclude[item.ID] {
			return false
		}
	}
	return true
}

// cooldownWaitLimit 是没有可选成员时单次等待的上限。
// 冷却通常长达一分钟, 按重试间隔反复醒来只是空转; 但也不能一睡到底,
// 否则人工改配置(切渠道, 补成员, 关冷却)要等到冷却到期才生效。
const cooldownWaitLimit = 10 * time.Second

// groupSaturated 判断"本轮没有可选成员"是否因为成员的并发上限已满, 而不是冷却或成员被排除。
// 满员是瞬时的: 槽位通常在一两个请求的耗时内就会腾出, 调用方据此改用亚秒级短等待;
// 有成员在冷却时返回 false, 等待时长仍由冷却到期时间决定。
func groupSaturated(group model.Group, exclude map[int]bool) bool {
	routeMu.Lock()
	defer routeMu.Unlock()

	route := groupRouteLocked(group)
	now := time.Now().UnixMilli()
	considered := false
	for _, item := range group.Items {
		if exclude[item.ID] {
			continue
		}
		if deadline, cooling := route.Cooldowns[item.ID]; cooling && deadline > now {
			continue
		}
		considered = true
		if itemHasSlot(route, item) {
			return false
		}
	}
	return considered
}

// routeWaitSeconds 决定"本轮没有可选成员"时该等多久。
// 有成员在冷却就等到最早的一个到期, 让请求在成员恢复的瞬间被接住; 没有成员在冷却(例如成员刚被删除)
// 则退回配置的重试间隔。上限是 cooldownWaitLimit, 保证配置变化最多 10 秒内生效。
func routeWaitSeconds(group model.Group, exclude map[int]bool) int {
	remaining := earliestCooldownRemaining(group, exclude)
	if remaining <= 0 {
		return group.RelayConfig.MemberRetryIntervalSeconds
	}
	// 向上取整, 避免提前醒来又立刻空转一轮。
	seconds := int((remaining + time.Second - 1) / time.Second)
	if seconds > int(cooldownWaitLimit/time.Second) {
		seconds = int(cooldownWaitLimit / time.Second)
	}
	if seconds < 1 {
		seconds = 1
	}
	return seconds
}

// earliestCooldownRemaining 返回分组内未被排除成员里最早的冷却剩余时长; 没有成员在冷却时返回 0。
func earliestCooldownRemaining(group model.Group, exclude map[int]bool) time.Duration {
	if group.Mode == model.GroupModeManual {
		return 0
	}

	routeMu.Lock()
	defer routeMu.Unlock()

	route := routes[group.ID]
	if route == nil {
		return 0
	}
	now := time.Now().UnixMilli()
	var earliest int64
	for _, item := range group.Items {
		if exclude[item.ID] {
			continue
		}
		deadline, cooling := route.Cooldowns[item.ID]
		if !cooling || deadline <= now {
			continue
		}
		if earliest == 0 || deadline < earliest {
			earliest = deadline
		}
	}
	if earliest == 0 {
		return 0
	}
	return time.Duration(earliest-now) * time.Millisecond
}

// provisionalCooldown 是确定性 4xx 的临时冷却时长。
// 它只需要覆盖"同一批并发请求都在飞"的窗口: 请求成功换成员后立刻升级为正式冷却,
// 请求本身有问题时会在所有成员都试过后整条撤销, 因此这个值只是兜底上限。
const provisionalCooldown = 15 * time.Second

// deterministicCooldownFloor 是确定性 4xx 成员被确认故障后的冷却下限。
// 这类成员对同一请求反复拒绝(凭据失效, 订阅过期, 模型不在该渠道), 恢复通常不会发生在配置的短冷却内:
// 按默认 60 秒冷却, 每个冷却周期都会有一个新请求先撞它一次再换人。用 5 分钟下限把这个代价压到可忽略,
// 同时又不至于长到上游续订后无法自行恢复。
const deterministicCooldownFloor = 5 * time.Minute

// deterministicCooldown 返回确定性 4xx 成员被确认故障后的冷却时长, 取配置值与下限中的较大者。
func deterministicCooldown(group model.Group) time.Duration {
	return max(time.Duration(group.RelayConfig.MemberCooldownSeconds)*time.Second, deterministicCooldownFloor)
}

// markRouteSuspect 给返回确定性 4xx 的成员压一个临时冷却, 让同批并发请求立刻绕开它。
func markRouteSuspect(group model.Group, itemID int) {
	if group.Mode == model.GroupModeManual {
		return
	}

	routeMu.Lock()
	defer routeMu.Unlock()

	route := groupRouteLocked(group)
	route.Cooldowns[itemID] = time.Now().Add(provisionalCooldown).UnixMilli()
	if route.provisional == nil {
		route.provisional = make(map[int]bool)
	}
	route.provisional[itemID] = true
	publishRouteLocked(route)
}

// clearRouteSuspects 撤销临时冷却: 分组内每个成员都对同一请求给出了确定性拒绝, 说明问题在请求本身,
// 不该把成员留在冷却里。正式冷却不受影响。
func clearRouteSuspects(group model.Group, itemIDs map[int]bool) {
	if group.Mode == model.GroupModeManual || len(itemIDs) == 0 {
		return
	}

	routeMu.Lock()
	defer routeMu.Unlock()

	route := routes[group.ID]
	if route == nil {
		return
	}
	changed := false
	for itemID := range itemIDs {
		if route.provisional[itemID] {
			delete(route.provisional, itemID)
			delete(route.Cooldowns, itemID)
			changed = true
		}
	}
	if changed {
		publishRouteLocked(route)
	}
}

// recordRouteSuccess 上报一轮成功: 结束该成员的冷却与探测占用, 并在故障切换后按配置开始亲和。
func recordRouteSuccess(group model.Group, itemID int) {
	if group.Mode == model.GroupModeManual {
		return
	}

	routeMu.Lock()
	defer routeMu.Unlock()

	route := routes[group.ID]
	if route == nil {
		return
	}
	now := time.Now().UnixMilli()
	changed := false

	// 探测成功说明该成员已恢复, 解除冷却; 若当前路由不在亲和期内则立即切回该成员。
	if route.ProbeItemID == itemID {
		route.ProbeItemID = 0
		delete(route.Cooldowns, itemID)
		if route.CurrentItemID == 0 || route.AffinityUntil <= now {
			route.CurrentItemID = itemID
			route.AffinityUntil = 0
		}
		changed = true
	}
	// 亲和只在故障切换后的首次成功时开始, 使请求在一段时间内稳定留在备用成员上; 轮询模式不做亲和。
	if group.Mode == model.GroupModeFailover && route.CurrentItemID == itemID && route.affinityArmed {
		route.affinityArmed = false
		if group.RelayConfig.MemberAffinitySeconds > 0 {
			route.AffinityUntil = now + int64(group.RelayConfig.MemberAffinitySeconds)*1000
			changed = true
		}
	}
	if changed {
		publishRouteLocked(route)
	}
}

// recordRouteFailure 上报一轮失败: 达到配置的总尝试次数后将该成员打入冷却并让出当前路由, 返回是否已冷却。
// failures 为该成员在本请求内的累计失败次数, 由调用方按成员分别累计; 轮询模式下成员会交替,
// 按成员累计才能让"同一成员反复失败"真正触发冷却。
// cooldown 非零时用它代替分组配置的冷却时长, 用于上游明确给出的重试间隔(429 的 Retry-After)。
func recordRouteFailure(group model.Group, itemID, failures int, cooldown time.Duration) bool {
	if group.Mode == model.GroupModeManual {
		return false
	}

	routeMu.Lock()
	defer routeMu.Unlock()

	route := routes[group.ID]
	if route == nil {
		return false
	}
	// 探测请求只有一次机会, 常规成员达到配置的总尝试次数后进入冷却。
	if route.ProbeItemID != itemID && failures < group.RelayConfig.MemberMaxAttempts {
		return false
	}
	if cooldown <= 0 {
		cooldown = time.Duration(group.RelayConfig.MemberCooldownSeconds) * time.Second
	}

	now := time.Now().UnixMilli()
	route.Cooldowns[itemID] = now + cooldown.Milliseconds()
	// 正式冷却接管临时冷却, 之后只有冷却到期才会重新放行探测。
	delete(route.provisional, itemID)
	if route.ProbeItemID == itemID {
		route.ProbeItemID = 0
	}
	// 当前路由失败才需要下一个成员开始亲和; 独立探测失败不影响当前路由。
	// 轮询模式保留游标: 清零会让下一轮重新从首个成员开始, 使分摊失衡。
	if group.Mode == model.GroupModeFailover && route.CurrentItemID == itemID {
		route.CurrentItemID = 0
		route.AffinityUntil = 0
		route.affinityArmed = true
	}
	publishRouteLocked(route)
	return true
}

// releaseRouteProbe 归还未产生成败结论的探测占用, 用于请求被人工中止或客户端断开。
func releaseRouteProbe(group model.Group, itemID int) {
	routeMu.Lock()
	defer routeMu.Unlock()

	if route := routes[group.ID]; route != nil && route.ProbeItemID == itemID {
		route.ProbeItemID = 0
		publishRouteLocked(route)
	}
}

// groupRouteLocked 取出分组路由状态并清理已删除成员的残留; 调用方必须持有锁。
func groupRouteLocked(group model.Group) *RouteState {
	route := routes[group.ID]
	if route == nil {
		route = &RouteState{
			GroupID:     group.ID,
			Cooldowns:   make(map[int]int64),
			provisional: make(map[int]bool),
			inflight:    make(map[int]int),
			weights:     make(map[int]int),
		}
		routes[group.ID] = route
	}
	items := make(map[int]bool, len(group.Items))
	for _, item := range group.Items {
		items[item.ID] = true
	}
	for itemID := range route.Cooldowns {
		if !items[itemID] {
			delete(route.Cooldowns, itemID)
			delete(route.provisional, itemID)
		}
	}
	if route.ProbeItemID != 0 && !items[route.ProbeItemID] {
		route.ProbeItemID = 0
	}
	if route.CurrentItemID != 0 && !items[route.CurrentItemID] {
		route.CurrentItemID = 0
		route.AffinityUntil = 0
		route.affinityArmed = false
	}
	return route
}

// itemOf 返回分组内指定 ID 的成员, 不存在时返回零值。
func itemOf(group model.Group, itemID int) model.GroupItem {
	for _, item := range group.Items {
		if item.ID == itemID {
			return item
		}
	}
	return model.GroupItem{}
}

// publishRouteLocked 非阻塞发布路由状态, 连接拥塞时关闭它并交给客户端重连获取全量快照; 冷却表按值复制以免前端读到后续变更; 调用方必须持有锁。
func publishRouteLocked(route *RouteState) {
	message := *route
	message.Cooldowns = maps.Clone(route.Cooldowns)
	for stream := range routeStreams {
		select {
		case stream <- message:
		default:
			delete(routeStreams, stream)
			close(stream)
		}
	}
}

// OpenRouteStream 注册路由流连接, 返回后续增量通道。
// 不再返回快照: 分组读取接口已随分组带回当前路由状态, 前端由此拿到的初始值即全量, 连接只负责增量。
func OpenRouteStream() chan RouteState {
	routeMu.Lock()
	defer routeMu.Unlock()

	stream := make(chan RouteState, routeStreamBuffer)
	routeStreams[stream] = struct{}{}
	return stream
}

// CloseRouteStream 注销并关闭指定路由流连接。
func CloseRouteStream(stream chan RouteState) {
	routeMu.Lock()
	defer routeMu.Unlock()

	if _, exists := routeStreams[stream]; exists {
		delete(routeStreams, stream)
		close(stream)
	}
}
