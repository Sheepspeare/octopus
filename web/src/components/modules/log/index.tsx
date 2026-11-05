import { useMemo, useState } from 'react';
import { Loader2, Logs } from 'lucide-react';
import { useTranslations } from 'use-intl';
import { useLogHistory, useLogs, type RelayLogOverview } from '@/api/log';
import { VirtualizedGrid } from '@/components/common/VirtualizedGrid';
import { Button } from '@/components/ui/button';
import { LogCard, LogDetailDialog } from './Item';

// Log 展示进程内日志概览，并按 RequestID 实时更新卡片。
// 状态流只推内存里的最近若干条，更早的历史按需从库里翻页取，两段按 ID 合并后统一渲染。
export function Log() {
    const t = useTranslations('log');
    const { logs, isLoading, error } = useLogs();
    const { older, load, isLoading: isLoadingOlder, exhausted } = useLogHistory();
    // 详情弹窗的打开状态由列表持有, 不放在卡片里: 虚拟列表会回收卡片, 卡片一卸载弹窗就跟着消失。
    const [openLog, setOpenLog] = useState<RelayLogOverview | null>(null);

    const merged = useMemo(() => {
        const byId = new Map<number, RelayLogOverview>();
        for (const item of older) byId.set(item.id, item);
        // 实时数据覆盖同 ID 的历史快照：状态流里的那条才是最新的。
        for (const item of logs) byId.set(item.id, item);
        return [...byId.values()].sort((a, b) => b.id - a.id);
    }, [logs, older]);

    // 打开中的记录跟随列表刷新, 让详情里的状态继续更新; 记录已被内存淘汰时退回打开时的快照, 弹窗不会因此关闭。
    const activeLog = useMemo(
        () => (openLog ? merged.find((item) => item.id === openLog.id) ?? openLog : null),
        [merged, openLog]
    );

    if (isLoading) {
        return (
            <div className="flex h-full items-center justify-center">
                <Loader2 className="size-6 animate-spin text-muted-foreground" />
            </div>
        );
    }

    if (merged.length === 0) {
        return (
            <div className="flex h-full flex-col items-center justify-center gap-3 text-muted-foreground">
                {!error && <Logs className="size-8" />}
                <span className="text-sm">{error ? t('list.disconnected') : t('list.empty')}</span>
            </div>
        );
    }

    return (
        <div className="flex h-full min-h-0 flex-col gap-3">
            {error && (
                <div className="flex shrink-0 items-center justify-center px-1 pb-3 text-xs text-destructive">
                    <span>{t('list.disconnected')}</span>
                </div>
            )}
            <div className="min-h-0 flex-1">
                <VirtualizedGrid
                    items={merged}
                    layout="list"
                    columns={{ default: 1 }}
                    estimateItemHeight={104}
                    overscan={8}
                    getItemKey={(log) => `log-${log.id}`}
                    renderItem={(log) => <LogCard log={log} onOpen={setOpenLog} />}
                />
            </div>
            {!exhausted && (
                <div className="flex shrink-0 justify-center pb-1">
                    <Button
                        variant="ghost"
                        size="sm"
                        disabled={isLoadingOlder}
                        onClick={() => load(merged[merged.length - 1].id)}
                        className="rounded-xl text-xs text-muted-foreground"
                    >
                        {isLoadingOlder ? <Loader2 className="size-3.5 animate-spin" /> : t('list.loadOlder')}
                    </Button>
                </div>
            )}
            <LogDetailDialog log={activeLog} onClose={() => setOpenLog(null)} />
        </div>
    );
}
