package relay

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/charmbracelet/log"
	"github.com/looplj/axonhub/llm"
	"gorm.io/gorm"
)

// 日志留存的三项限制; 任一项超限都会从最旧的记录开始裁剪。
const (
	logRetentionDays = 7              // 历史保留天数。
	logMaxRows       = 10000          // 库内最多保留的记录条数。
	logMaxDiskBytes  = int64(2) << 30 // 正文目录的总占用上限。
	logJanitorPeriod = time.Hour      // 裁剪任务的执行间隔。
	logBodyDirPerm   = 0o755          // 正文日期目录的权限。
	logBodyFilePerm  = 0o644          // 正文文件的权限。
	logDateLayout    = "2006-01-02"   // 日期目录名格式。
)

// logRoot 是正文文件的根目录, 由 InitStore 写入。
var logRoot string

// InitStore 准备正文目录并记录根路径; 目录不可用时日志仍可只走内存, 故失败只告警。
func InitStore(root string) {
	logRoot = root
	if err := os.MkdirAll(root, logBodyDirPerm); err != nil {
		log.Errorf("failed to create relay log dir %s: %v", root, err)
		return
	}
	log.Infof("Relay log dir: %s", root)
}

// bodyPath 生成一条正文的相对路径; 按请求到达日期分目录, 便于按天整目录删除。
func bodyPath(id uint64, startedAt time.Time, response bool) string {
	suffix := "req"
	if response {
		suffix = "resp"
	}
	return filepath.Join(startedAt.Format(logDateLayout), fmt.Sprintf("%d.%s", id, suffix))
}

// writeBody 把正文写到磁盘并返回相对路径; 内容为空时不落文件。
func writeBody(id uint64, startedAt time.Time, body string, response bool) string {
	if body == "" || logRoot == "" {
		return ""
	}
	relative := bodyPath(id, startedAt, response)
	absolute := filepath.Join(logRoot, relative)
	if err := os.MkdirAll(filepath.Dir(absolute), logBodyDirPerm); err != nil {
		log.Warnf("failed to create relay log subdir: %v", err)
		return ""
	}
	if err := os.WriteFile(absolute, []byte(body), logBodyFilePerm); err != nil {
		log.Warnf("failed to write relay log body %s: %v", relative, err)
		return ""
	}
	return relative
}

// readBody 按相对路径读回正文; 文件已被裁掉或路径为空时返回空串。
func readBody(relative string) string {
	if relative == "" || logRoot == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(logRoot, relative))
	if err != nil {
		return ""
	}
	return string(data)
}

// removeBody 删除一条记录的两个正文文件, 顺带清掉空出来的日期目录。
func removeBody(record model.RelayLog) {
	if logRoot == "" {
		return
	}
	for _, relative := range []string{record.RequestBodyPath, record.ResponseBodyPath} {
		if relative == "" {
			continue
		}
		absolute := filepath.Join(logRoot, relative)
		if err := os.Remove(absolute); err != nil && !os.IsNotExist(err) {
			log.Warnf("failed to remove relay log body %s: %v", relative, err)
		}
	}
}

// 待落盘的记录; 攒够一批或等够时间才真正写盘, 避免每条请求都单独一次事务。
type persistTask struct {
	record       model.RelayLog
	requestBody  string
	responseBody string
}

// 批量落盘与内存缓存的参数。
const (
	persistBatchSize  = 50               // 攒够多少条就立即写盘。
	persistBatchWait  = 2 * time.Second  // 不足一批时最长等这么久再写。
	maxPendingBytes   = int64(100) << 20 // 待落盘正文的内存上限, 超过即强制写盘。
	persistInsertSize = 100              // 单次事务插入的行数。
)

var (
	pendingMu    sync.Mutex
	pending      []persistTask
	pendingBytes int64
	flushSignal  = make(chan struct{}, 1) // 攒够一批时通知写盘协程, 非阻塞。
)

var (
	storeCtx, storeCancel = context.WithCancel(context.Background())
	persistDone           = make(chan struct{}) // 写盘协程退出时关闭, 供退出流程等待排空。
)

// StartPersistWorker 串行写盘与写库; 单写者既避免 SQLite 写锁争抢, 也保证文件与记录同序落定。
// 记录先攒在内存里, 攒够一批或等够时间再一次性落盘: 逐条写会让每次请求都带一次事务开销。
func StartPersistWorker() {
	go func() {
		defer close(persistDone)
		ticker := time.NewTicker(persistBatchWait)
		defer ticker.Stop()
		for {
			select {
			case <-storeCtx.Done():
				flushPending()
				return
			case <-flushSignal:
				flushPending()
			case <-ticker.C:
				flushPending()
			}
		}
	}()
}

// StopStore 停止写盘与裁剪协程, 并把内存里攒着的记录写完; 必须在关闭数据库之前调用。
func StopStore() error {
	storeCancel()
	<-persistDone
	return nil
}

// enqueuePersist 把一条已结束的请求放进待落盘缓冲; 攒够一批或超过内存上限时立刻触发写盘。
func enqueuePersist(record model.RelayLog, requestBody, responseBody string) {
	pendingMu.Lock()
	pending = append(pending, persistTask{record: record, requestBody: requestBody, responseBody: responseBody})
	pendingBytes += int64(len(requestBody) + len(responseBody))
	full := len(pending) >= persistBatchSize || pendingBytes >= maxPendingBytes
	pendingMu.Unlock()

	if full {
		select {
		case flushSignal <- struct{}{}:
		default:
		}
	}
}

// flushPending 取走当前缓冲并写盘; 没有待写记录时直接返回。
func flushPending() {
	pendingMu.Lock()
	batch := pending
	pending = nil
	pendingBytes = 0
	pendingMu.Unlock()

	if len(batch) == 0 {
		return
	}
	persistBatch(batch)
}

// persistBatch 先逐条写正文文件, 再用一次事务批量插入元数据。
// 正文按记录各成一个文件, 无法合并写入; 真正省下的是数据库往返与事务开销。
func persistBatch(batch []persistTask) {
	database := db.GetDB()
	if database == nil {
		return
	}
	createdAt := time.Now()
	records := make([]model.RelayLog, 0, len(batch))
	for _, task := range batch {
		record := task.record
		record.RequestBodyPath = writeBody(record.ID, record.StartedAt, task.requestBody, false)
		record.ResponseBodyPath = writeBody(record.ID, record.StartedAt, task.responseBody, true)
		record.RequestBodyBytes = int64(len(task.requestBody))
		record.ResponseBodyBytes = int64(len(task.responseBody))
		record.CreatedAt = createdAt
		records = append(records, record)
	}
	if err := database.CreateInBatches(&records, persistInsertSize).Error; err != nil {
		log.Warnf("failed to persist %d relay logs: %v", len(records), err)
	}
}

// RestoreRecent 把最近若干条记录读回内存, 使重启后的日志页仍能看到历史。
// 正文不进内存, 打开详情时再按路径读取。
func RestoreRecent() {
	database := db.GetDB()
	if database == nil {
		return
	}
	var records []model.RelayLog
	if err := database.Order("id desc").Limit(maxInMemory).Find(&records).Error; err != nil {
		log.Warnf("failed to restore relay logs: %v", err)
		return
	}
	if len(records) == 0 {
		return
	}

	mu.Lock()
	defer mu.Unlock()
	for _, record := range records {
		if _, exists := requests[record.ID]; exists {
			continue
		}
		requests[record.ID] = recordToState(record)
		if record.ID > idSeq.Load() {
			idSeq.Store(record.ID)
		}
	}
	log.Infof("Restored %d relay log records", len(records))
}

// recordToState 把持久化记录还原成内存态; 正文留空, 读取时回落到文件。
func recordToState(record model.RelayLog) *RequestState {
	return &RequestState{
		ID:                 record.ID,
		Status:             Status(record.Status),
		StartedAt:          record.StartedAt,
		Duration:           record.Duration,
		FirstTokenDuration: record.FirstTokenDuration,
		StreamDuration:     record.StreamDuration,
		ResponseDuration:   record.ResponseDuration,
		Model:              record.Model,
		ReasoningEffort:    record.ReasoningEffort,
		Protocol:           model.Protocol(record.Protocol),
		GroupID:            record.GroupID,
		APIKeyName:         record.APIKeyName,
		Usage: llm.Usage{
			PromptTokens:     record.PromptTokens,
			CompletionTokens: record.CompletionTokens,
			TotalTokens:      record.TotalTokens,
		},
		Cost:             record.Cost,
		Round:            record.Round,
		TargetChannelKey: record.TargetChannelKey,
		TargetModel:      record.TargetModel,
		TargetProtocol:   model.Protocol(record.TargetProtocol),
		Error:            record.Error,
	}
}

// StateOf 把持久化记录还原成状态流的消息形状, 供分页接口复用同一份前端类型。
func StateOf(record model.RelayLog) RequestState {
	return *recordToState(record)
}

// ListPersisted 按 ID 倒序读取已落库的历史; before 为 0 表示从最新一条开始。
func ListPersisted(before uint64, limit int) ([]model.RelayLog, error) {
	database := db.GetDB()
	if database == nil {
		return nil, nil
	}
	query := database.Order("id desc").Limit(limit)
	if before > 0 {
		query = query.Where("id < ?", before)
	}
	var records []model.RelayLog
	err := query.Find(&records).Error
	return records, err
}

// readPersistedBody 按记录里的路径读取正文; 记录不存在或未留存正文时返回空串。
func readPersistedBody(id uint64, response bool) string {
	database := db.GetDB()
	if database == nil {
		return ""
	}
	var record model.RelayLog
	if err := database.Select("request_body_path", "response_body_path").First(&record, id).Error; err != nil {
		return ""
	}
	if response {
		return readBody(record.ResponseBodyPath)
	}
	return readBody(record.RequestBodyPath)
}

// StartLogJanitor 定期裁剪历史; 首次在启动后立即跑一次, 把上次停机期间过期的记录清掉。
func StartLogJanitor() {
	go func() {
		pruneLogs()
		ticker := time.NewTicker(logJanitorPeriod)
		defer ticker.Stop()
		for {
			select {
			case <-storeCtx.Done():
				return
			case <-ticker.C:
				pruneLogs()
			}
		}
	}()
}

// pruneLogs 按保留期, 条数与磁盘占用三项限制裁剪历史, 删除记录时同步删掉正文文件。
func pruneLogs() {
	database := db.GetDB()
	if database == nil {
		return
	}

	cutoff := time.Now().AddDate(0, 0, -logRetentionDays)
	var expired []model.RelayLog
	database.Where("created_at < ?", cutoff).Find(&expired)
	for _, record := range expired {
		removeBody(record)
	}
	if len(expired) > 0 {
		database.Where("created_at < ?", cutoff).Delete(&model.RelayLog{})
		// 日期目录里可能还有已被单独删除的记录留下的空目录, 一并清掉。
		removeExpiredDirs(cutoff)
	}

	// 条数上限: 多出来的部分按 ID 从旧到新删, 一次查全再批量删, 避免逐条往返。
	var count int64
	database.Model(&model.RelayLog{}).Count(&count)
	if count > logMaxRows {
		var stale []model.RelayLog
		database.Order("id asc").Limit(int(count - logMaxRows)).Find(&stale)
		deleteRecords(database, stale)
	}

	// 磁盘上限: 正文才是占空间的部分, 条数达标后仍可能超限, 故再按体积裁一遍。
	for {
		size := bodyDiskUsage()
		if size <= logMaxDiskBytes {
			break
		}
		var oldest []model.RelayLog
		database.Order("id asc").Limit(200).Find(&oldest)
		if len(oldest) == 0 {
			break
		}
		deleteRecords(database, oldest)
	}
}

// deleteRecords 删掉一批记录的正文文件与库内行。
func deleteRecords(database *gorm.DB, records []model.RelayLog) {
	if len(records) == 0 {
		return
	}
	ids := make([]uint64, 0, len(records))
	for _, record := range records {
		removeBody(record)
		ids = append(ids, record.ID)
	}
	database.Where("id IN ?", ids).Delete(&model.RelayLog{})
}

// removeExpiredDirs 删除保留期之前的日期目录; 目录名即日期, 无需逐文件比对。
func removeExpiredDirs(cutoff time.Time) {
	if logRoot == "" {
		return
	}
	entries, err := os.ReadDir(logRoot)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		date, err := time.ParseInLocation(logDateLayout, entry.Name(), time.Local)
		if err != nil {
			continue
		}
		// 目录名的日期当天还有效就保留, 故按天边界比较而不是按 cutoff 的时刻。
		if date.AddDate(0, 0, logRetentionDays).Before(time.Now()) {
			_ = os.RemoveAll(filepath.Join(logRoot, entry.Name()))
		}
	}
}

// bodyDiskUsage 统计正文目录的总字节数, 用于磁盘上限裁剪。
func bodyDiskUsage() int64 {
	if logRoot == "" {
		return 0
	}
	var total int64
	_ = filepath.Walk(logRoot, func(_ string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		total += info.Size()
		return nil
	})
	return total
}

// ClearPersisted 删除全部历史记录与正文文件, 供手动清空使用。
func ClearPersisted() {
	database := db.GetDB()
	if database == nil {
		return
	}
	database.Where("1 = 1").Delete(&model.RelayLog{})
	if logRoot == "" {
		return
	}
	entries, err := os.ReadDir(logRoot)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() {
			_ = os.RemoveAll(filepath.Join(logRoot, entry.Name()))
		}
	}
}

// LogStats 汇总当前留存情况, 供界面显示磁盘占用。
func LogStats() (rows int64, bytes int64) {
	database := db.GetDB()
	if database != nil {
		database.Model(&model.RelayLog{}).Count(&rows)
	}
	return rows, bodyDiskUsage()
}
