import { useId, useState } from 'react';
import { useTranslations } from 'use-intl';
import {
    KeyRound,
    Plus,
    Loader,
    Trash2,
    Check,
    X,
    Info,
    CalendarDays,
    Pencil,
    Maximize2,
    PlugZap,
    CheckCircle2,
    XCircle,
    RefreshCw,
} from 'lucide-react';
import { motion, AnimatePresence } from 'motion/react';
import { Input } from '@/components/ui/input';
import { Calendar } from '@/components/ui/calendar';
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover';
import { Switch } from '@/components/ui/switch';
import { Badge } from '@/components/ui/badge';
import {
    MorphingDialog,
    MorphingDialogContainer,
    MorphingDialogContent,
    MorphingDialogTrigger,
    useMorphingDialog,
} from '@/components/ui/morphing-dialog';
import {
    useAPIKeyList,
    useCreateAPIKey,
    useUpdateAPIKey,
    useDeleteAPIKey,
    useTestAPIKeyModels,
    useTestAPIKeyChat,
    type APIKey,
    type APIKeyTestResult,
} from '@/api/apikey';
import { useGroupList } from '@/api/group';
import { useStatsAPIKey } from '@/api/stats';
import { cn } from '@/lib/utils';
import { toast } from 'sonner';
import { CopyIconButton } from '@/components/common/CopyButton';

function toExpireAt(date: Date, time: string): number {
    const t = /^\d{2}:\d{2}$/.test(time) ? time : '00:00';
    const [hh, mm] = t.split(':').map(Number);
    const d = new Date(Date.UTC(date.getFullYear(), date.getMonth(), date.getDate(), hh, mm, 0));
    // 返回 Unix 时间戳（秒）
    return Math.floor(d.getTime() / 1000);
}

function parseExpireDate(expireAt?: number): Date | undefined {
    if (!expireAt) return undefined;
    // 从 Unix 时间戳（秒）转换为 Date
    const d = new Date(expireAt * 1000);
    return isNaN(d.getTime()) ? undefined : d;
}

function normalizeHHmm(input: string): string {
    const cleaned = input.replace(/[^\d:]/g, '');
    const parts = cleaned.includes(':') ? cleaned.split(':') : [cleaned.slice(0, 2), cleaned.slice(2, 4)];
    const hh = Math.min(23, Math.max(0, parseInt(parts[0] || '0', 10)));
    const mm = Math.min(59, Math.max(0, parseInt(parts[1] || '0', 10)));
    return `${hh.toString().padStart(2, '0')}:${mm.toString().padStart(2, '0')}`;
}

function normalizeMoneyInput(input: string): string {
    const cleaned = input.replace(/[^\d.]/g, '');
    const [intPart, ...rest] = cleaned.split('.');
    return rest.length > 0 ? `${intPart}.${rest.join('').slice(0, 6)}` : intPart;
}

type APIKeyFormData = Omit<APIKey, 'id'>;

interface APIKeyFormProps {
    apiKey?: APIKey;
    isPending: boolean;
    submitLabel: string;
    onSubmit: (data: APIKeyFormData) => void;
    onClose: () => void;
}

function APIKeyForm({ apiKey, isPending, submitLabel, onSubmit, onClose }: APIKeyFormProps) {
    const t = useTranslations('setting');
    const { data: groups = [] } = useGroupList();

    const [form, setForm] = useState<APIKeyFormData>(() => ({
        name: apiKey?.name ?? '',
        api_key: apiKey?.api_key ?? '',
        enabled: apiKey?.enabled ?? true,
        expire_at: apiKey?.expire_at,
        max_cost: apiKey?.max_cost,
        supported_models: apiKey?.supported_models ?? [],
    }));
    const [maxCostInput, setMaxCostInput] = useState(() =>
        apiKey?.max_cost != null ? String(apiKey.max_cost) : ''
    );
    const [expireTime, setExpireTime] = useState(() => {
        if (apiKey?.expire_at) {
            const d = new Date(apiKey.expire_at * 1000);
            if (!isNaN(d.getTime())) {
                return `${d.getUTCHours().toString().padStart(2, '0')}:${d.getUTCMinutes().toString().padStart(2, '0')}`;
            }
        }
        return '00:00';
    });
    const [expireOpen, setExpireOpen] = useState(false);

    const availableModels = Array.from(new Set(groups.map((g) => g.name).filter(Boolean)));
    const supportedModels = new Set(form.supported_models);

    const expireDate = parseExpireDate(form.expire_at);
    const neverExpire = !form.expire_at;
    const isUnlimitedCost = maxCostInput.trim() === '';

    const expireLabel = neverExpire
        ? t('apiKey.form.neverExpire')
        : expireDate
            ? expireDate.toLocaleDateString()
            : t('apiKey.form.selectDate');

    const updateForm = (updater: Partial<APIKeyFormData>) => {
        setForm((prev) => ({ ...prev, ...updater }));
    };

    const handleSelectDate = (d: Date | undefined) => {
        if (d) {
            updateForm({ expire_at: toExpireAt(d, expireTime) });
            setExpireOpen(false);
        } else {
            updateForm({ expire_at: undefined });
        }
    };

    const handleTimeBlur = () => {
        if (!expireDate) return;
        const normalized = normalizeHHmm(expireTime);
        setExpireTime(normalized);
        updateForm({ expire_at: toExpireAt(expireDate, normalized) });
    };

    const handleToggleNeverExpire = () => {
        if (neverExpire) {
            updateForm({ expire_at: toExpireAt(new Date(), expireTime) });
        } else {
            updateForm({ expire_at: undefined });
            setExpireOpen(false);
        }
    };

    const handleMaxCostChange = (val: string) => {
        const normalized = normalizeMoneyInput(val);
        setMaxCostInput(normalized);
        const num = parseFloat(normalized);
        updateForm({ max_cost: Number.isFinite(num) ? num : undefined });
    };

    const handleClearMaxCost = () => {
        setMaxCostInput('');
        updateForm({ max_cost: undefined });
    };

    const handleSubmit = (e: React.FormEvent) => {
        e.preventDefault();
        if (!form.name.trim()) return;
        onSubmit(form);
    };

    return (
        <form onSubmit={handleSubmit} className="grid gap-2">
            <label className="grid gap-1 text-xs text-muted-foreground">
                {t('apiKey.form.name')}
                <Input
                    type="text"
                    value={form.name}
                    onChange={(e) => updateForm({ name: e.target.value })}
                    className="h-9 text-sm rounded-xl"
                    disabled={isPending}
                    required
                />
            </label>

            <label className="grid gap-1 text-xs text-muted-foreground">
                {t('apiKey.form.value')}
                <Input
                    type="text"
                    value={form.api_key}
                    onChange={(e) => updateForm({ api_key: e.target.value })}
                    placeholder={t('apiKey.form.valuePlaceholder')}
                    className="h-9 text-sm rounded-xl"
                    disabled={isPending}
                />
            </label>

            <div className="grid gap-1 text-xs text-muted-foreground">
                {t('apiKey.form.maxCost')}
                <div className="flex items-center gap-2">
                    <div className="relative flex-1">
                        <span className="absolute left-3 top-1/2 -translate-y-1/2 text-sm text-muted-foreground">$</span>
                        <Input
                            type="text"
                            inputMode="decimal"
                            placeholder={t('apiKey.form.maxCostPlaceholder')}
                            value={maxCostInput}
                            onChange={(e) => handleMaxCostChange(e.target.value)}
                            className="h-9 text-sm rounded-xl pl-7"
                            disabled={isPending}
                        />
                    </div>
                    <button
                        type="button"
                        onClick={handleClearMaxCost}
                        disabled={isPending}
                        aria-pressed={isUnlimitedCost}
                        className={cn(
                            'h-9 px-3 rounded-xl border text-sm transition-colors shrink-0',
                            isUnlimitedCost
                                ? 'bg-primary text-primary-foreground border-primary/30'
                                : 'border-border bg-muted/20 text-foreground hover:bg-muted/30',
                            isPending && 'opacity-50 cursor-not-allowed'
                        )}
                    >
                        {t('apiKey.form.unlimited')}
                    </button>
                </div>
            </div>

            <div className="grid gap-1 text-xs text-muted-foreground">
                {t('apiKey.form.expireAt')}
                <div className="flex items-center gap-2 relative">
                    <Popover
                        open={expireOpen && !neverExpire}
                        onOpenChange={setExpireOpen}
                    >
                        <PopoverTrigger asChild>
                            <button
                                type="button"
                                disabled={isPending || neverExpire}
                                className="h-9 flex-1 flex items-center justify-between gap-2 rounded-xl border border-border bg-muted/20 px-3 text-sm text-foreground transition-colors hover:bg-muted/30 disabled:opacity-50"
                            >
                                <span className="truncate">{expireLabel}</span>
                                <CalendarDays className="size-4 text-muted-foreground" />
                            </button>
                        </PopoverTrigger>
                        <PopoverContent
                            align="start"
                            side="bottom"
                            sideOffset={8}
                            className="w-fit rounded-2xl border border-border/60 shadow-xl overflow-hidden bg-card p-0"
                        >
                            <Calendar
                                mode="single"
                                selected={expireDate}
                                onSelect={handleSelectDate}
                                disabled={isPending}
                                classNames={{ today: '' }}
                            />
                        </PopoverContent>
                    </Popover>

                    <Input
                        type="text"
                        value={expireTime}
                        onChange={(e) => setExpireTime(e.target.value.replace(/[^\d:]/g, '').slice(0, 5))}
                        onBlur={handleTimeBlur}
                        className="h-9 w-[92px] text-sm rounded-xl"
                        disabled={isPending || neverExpire || !expireDate}
                        inputMode="numeric"
                        placeholder="HH:mm"
                    />

                    <button
                        type="button"
                        onClick={handleToggleNeverExpire}
                        disabled={isPending}
                        aria-pressed={neverExpire}
                        className={cn(
                            'h-9 px-3 rounded-xl border text-sm transition-colors whitespace-nowrap shrink-0',
                            neverExpire
                                ? 'bg-primary text-primary-foreground border-primary/30'
                                : 'border-border bg-muted/20 text-foreground hover:bg-muted/30',
                            isPending && 'opacity-50 cursor-not-allowed'
                        )}
                    >
                        {t('apiKey.form.neverExpire')}
                    </button>
                </div>
            </div>

            <div className="grid gap-1">
                <div className="text-xs text-muted-foreground">{t('apiKey.form.supportedModels')}</div>
                <div className="max-h-40 overflow-auto rounded-xl p-2">
                    {availableModels.length === 0 ? (
                        <div className="text-xs text-muted-foreground py-2 text-center">
                            {t('apiKey.form.noModels')}
                        </div>
                    ) : (
                        <div className="flex flex-wrap gap-2">
                            {availableModels.map((m) => {
                                const checked = supportedModels.has(m);
                                return (
                                    <button
                                        key={m}
                                        type="button"
                                        disabled={isPending}
                                        onClick={() => updateForm({
                                            supported_models: checked
                                                ? form.supported_models.filter((name) => name !== m)
                                                : [...form.supported_models, m],
                                        })}
                                        className="text-left disabled:opacity-50"
                                    >
                                        <Badge
                                            variant={checked ? 'default' : 'outline'}
                                            className={cn(
                                                'cursor-pointer select-none',
                                                !checked && 'bg-background/40 hover:bg-background/70'
                                            )}
                                        >
                                            {m}
                                        </Badge>
                                    </button>
                                );
                            })}
                        </div>
                    )}
                </div>
                <div className="text-[11px] text-muted-foreground/80">{t('apiKey.form.modelsHint')}</div>
            </div>

            <div className="flex items-center justify-between pt-1">
                <span className="text-xs text-muted-foreground">{t('apiKey.form.enabled')}</span>
                <Switch
                    checked={form.enabled ?? true}
                    onCheckedChange={(checked) => updateForm({ enabled: checked })}
                    disabled={isPending}
                />
            </div>

            <div className="flex gap-2 pt-2 mt-3">
                <button
                    type="button"
                    onClick={onClose}
                    disabled={isPending}
                    className="flex-1 h-9 flex items-center justify-center gap-1.5 rounded-xl bg-muted text-muted-foreground text-sm font-medium transition-all hover:bg-muted/80 active:scale-[0.98] disabled:opacity-50"
                >
                    <X className="size-4" />
                    {t('apiKey.form.cancel')}
                </button>
                <button
                    type="submit"
                    disabled={isPending || !form.name.trim()}
                    className="flex-1 h-9 flex items-center justify-center gap-1.5 rounded-xl bg-primary text-primary-foreground text-sm font-medium transition-all hover:bg-primary/90 active:scale-[0.98] disabled:opacity-50"
                >
                    {isPending ? <Loader className="size-4 animate-spin" /> : <Check className="size-4" />}
                    {submitLabel}
                </button>
            </div>
        </form>
    );
}

function APIKeyFormOverlay({
    layoutId,
    apiKey,
    isPending,
    submitLabel,
    onSubmit,
    onClose,
}: {
    layoutId: string;
    apiKey?: APIKey;
    isPending: boolean;
    submitLabel: string;
    onSubmit: (data: APIKeyFormData) => void;
    onClose: () => void;
}) {
    return (
        <motion.div
            layoutId={layoutId}
            className="absolute left-1/2 top-1/2 z-20 w-[min(420px,calc(100vw-2rem))] -translate-x-1/2 -translate-y-1/2 bg-card p-5 rounded-3xl border border-border max-h-[80vh] overflow-auto"
            transition={{ type: 'spring', stiffness: 400, damping: 30 }}
        >
            <APIKeyForm
                apiKey={apiKey}
                isPending={isPending}
                submitLabel={submitLabel}
                onSubmit={onSubmit}
                onClose={onClose}
            />
        </motion.div>
    );
}

function APIKeyStatsCard({
    layoutId,
    apiKey,
    onClose,
}: {
    layoutId: string;
    apiKey: APIKey;
    onClose: () => void;
}) {
    const t = useTranslations('setting');
    const { data: statsList = [] } = useStatsAPIKey();
    const stats = statsList.find((s) => s.api_key_id === apiKey.id);

    return (
        <motion.div
            layoutId={layoutId}
            className="absolute left-1/2 top-1/2 z-30 w-[min(320px,calc(100vw-2rem))] -translate-x-1/2 -translate-y-1/2 flex flex-col bg-card p-5 rounded-3xl border border-border max-h-[80vh] overflow-auto"
            transition={{ type: 'spring', stiffness: 400, damping: 30 }}
        >
            <div className="flex items-center justify-between gap-2 mb-3">
                <h3 className="text-sm font-semibold text-card-foreground line-clamp-1">
                    {apiKey.name}
                </h3>
                <button
                    type="button"
                    onClick={onClose}
                    className="size-8 flex items-center justify-center rounded-lg bg-muted text-muted-foreground transition-colors hover:bg-muted/80"
                >
                    <X className="size-4" />
                </button>
            </div>

            {!stats ? (
                <div className="text-sm text-muted-foreground">{t('apiKey.stats.noData')}</div>
            ) : (
                <div className="grid grid-cols-2 gap-3 text-sm">
                    <div className="rounded-lg bg-muted/40 p-3">
                        <div className="text-xs text-muted-foreground">{t('apiKey.stats.inputToken')}</div>
                        <div className="font-medium tabular-nums">
                            {stats.input_token.formatted.value}
                            {stats.input_token.formatted.unit}
                        </div>
                    </div>
                    <div className="rounded-lg bg-muted/40 p-3">
                        <div className="text-xs text-muted-foreground">{t('apiKey.stats.outputToken')}</div>
                        <div className="font-medium tabular-nums">
                            {stats.output_token.formatted.value}
                            {stats.output_token.formatted.unit}
                        </div>
                    </div>
                    <div className="rounded-lg bg-muted/40 p-3">
                        <div className="text-xs text-muted-foreground">{t('apiKey.stats.inputCost')}</div>
                        <div className="font-medium tabular-nums">
                            {stats.input_cost.formatted.value}
                            {stats.input_cost.formatted.unit}
                        </div>
                    </div>
                    <div className="rounded-lg bg-muted/40 p-3">
                        <div className="text-xs text-muted-foreground">{t('apiKey.stats.outputCost')}</div>
                        <div className="font-medium tabular-nums">
                            {stats.output_cost.formatted.value}
                            {stats.output_cost.formatted.unit}
                        </div>
                    </div>
                    <div className="rounded-lg bg-muted/40 p-3">
                        <div className="text-xs text-muted-foreground">{t('apiKey.stats.requestSuccess')}</div>
                        <div className="font-medium tabular-nums">
                            {stats.request_success.formatted.value}
                            {stats.request_success.formatted.unit}
                        </div>
                    </div>
                    <div className="rounded-lg bg-muted/40 p-3">
                        <div className="text-xs text-muted-foreground">{t('apiKey.stats.requestFailed')}</div>
                        <div className="font-medium tabular-nums">
                            {stats.request_failed.formatted.value}
                            {stats.request_failed.formatted.unit}
                        </div>
                    </div>
                </div>
            )}
        </motion.div>
    );
}

// APIKeyTestPopover 外部调用测试: 用这把钥匙打本机 /v1 入口, 先列出可用模型, 再按模型发真实补全。
// 模型清单来自 /v1/models 而非本地分组缓存, 因为这里要验证的正是「外部客户端能看见什么」。
function APIKeyTestPopover({ apiKey }: { apiKey: APIKey }) {
    const t = useTranslations('setting');
    const testModels = useTestAPIKeyModels();
    const testChat = useTestAPIKeyChat();

    const [open, setOpen] = useState(false);
    const [list, setList] = useState<APIKeyTestResult | null>(null);
    const [results, setResults] = useState<Record<string, APIKeyTestResult>>({});
    const [testing, setTesting] = useState<string | null>(null);
    const [lastModel, setLastModel] = useState<string | null>(null);

    const loadModels = () => {
        setList(null);
        testModels.mutate(apiKey.id, { onSuccess: (data) => setList(data) });
    };

    const handleOpenChange = (next: boolean) => {
        setOpen(next);
        if (next) loadModels();
    };

    const runChat = (model: string) => {
        setTesting(model);
        setLastModel(model);
        testChat.mutate({ id: apiKey.id, model }, {
            onSuccess: (data) => setResults((prev) => ({ ...prev, [model]: data })),
            onSettled: () => setTesting(null),
        });
    };

    const detail = lastModel ? results[lastModel] : undefined;

    return (
        <Popover open={open} onOpenChange={handleOpenChange}>
            <PopoverTrigger asChild>
                <button
                    type="button"
                    aria-label={t('apiKey.test.title')}
                    className="flex size-8 items-center justify-center rounded-lg bg-muted/60 text-muted-foreground transition-colors hover:bg-muted hover:text-foreground active:scale-95"
                >
                    <PlugZap className="size-4" />
                </button>
            </PopoverTrigger>
            <PopoverContent
                align="end"
                sideOffset={8}
                className="w-80 rounded-2xl border border-border/60 bg-card p-3 shadow-xl"
            >
                <div className="mb-2 flex items-center justify-between gap-2">
                    <span className="text-xs font-medium text-card-foreground">{t('apiKey.test.title')}</span>
                    <button
                        type="button"
                        onClick={loadModels}
                        disabled={testModels.isPending}
                        aria-label={t('apiKey.test.refresh')}
                        className="flex size-6 items-center justify-center rounded-md text-muted-foreground/70 transition-colors hover:text-foreground disabled:opacity-50"
                    >
                        <RefreshCw className={cn('size-3.5', testModels.isPending && 'animate-spin')} />
                    </button>
                </div>

                {list?.base_url && (
                    <div className="mb-2 rounded-lg bg-muted/50 px-2 py-1">
                        <div className="text-[10px] text-muted-foreground">{t('apiKey.test.baseUrl')}</div>
                        <code className="block truncate text-[11px] text-card-foreground">{list.base_url}</code>
                    </div>
                )}

                {testModels.isPending || !list ? (
                    <div className="flex items-center justify-center gap-2 py-5 text-xs text-muted-foreground">
                        <Loader className="size-3.5 animate-spin" />
                        {t('apiKey.test.modelsLoading')}
                    </div>
                ) : !list.ok ? (
                    <div className="space-y-1">
                        <div className="flex items-center gap-1.5 text-xs text-destructive">
                            <XCircle className="size-3.5" />
                            {t('apiKey.test.fetchFailed')}
                            {list.status > 0 && <span className="tabular-nums">· HTTP {list.status}</span>}
                        </div>
                        <p className="max-h-28 overflow-auto break-all rounded-lg bg-destructive/5 p-2 text-[11px] text-muted-foreground">
                            {list.error || list.body}
                        </p>
                    </div>
                ) : (
                    <div className="max-h-56 space-y-0.5 overflow-y-auto">
                        {list.models?.map((model) => {
                            const result = results[model];
                            const isTesting = testing === model;
                            return (
                                <button
                                    key={model}
                                    type="button"
                                    onClick={() => runChat(model)}
                                    disabled={testing !== null}
                                    className="flex w-full items-center justify-between gap-2 rounded-lg px-2 py-1.5 text-left transition-colors hover:bg-muted/60 disabled:opacity-60"
                                >
                                    <span className="truncate text-xs text-card-foreground">{model}</span>
                                    <span className="flex shrink-0 items-center gap-1 text-[11px] tabular-nums">
                                        {isTesting ? (
                                            <Loader className="size-3.5 animate-spin text-muted-foreground" />
                                        ) : result?.ok ? (
                                            <>
                                                <CheckCircle2 className="size-3.5 text-emerald-600" />
                                                <span className="text-emerald-600">{result.latency_ms}ms</span>
                                            </>
                                        ) : result ? (
                                            <>
                                                <XCircle className="size-3.5 text-destructive" />
                                                <span className="text-destructive">{result.status || 'ERR'}</span>
                                            </>
                                        ) : (
                                            <PlugZap className="size-3.5 text-muted-foreground/50" />
                                        )}
                                    </span>
                                </button>
                            );
                        })}
                    </div>
                )}

                {detail && (
                    <div className="mt-2 rounded-lg border border-border/60 p-2">
                        <div
                            className={cn(
                                'mb-1 flex items-center gap-1 text-[11px] font-medium',
                                detail.ok ? 'text-emerald-600' : 'text-destructive'
                            )}
                        >
                            {detail.ok ? <CheckCircle2 className="size-3.5" /> : <XCircle className="size-3.5" />}
                            {detail.ok
                                ? t('apiKey.test.ok', { ms: detail.latency_ms })
                                : t('apiKey.test.fail', { status: detail.status })}
                        </div>
                        <p className="max-h-24 overflow-auto break-all text-[11px] leading-4 text-muted-foreground">
                            {detail.ok ? detail.content : detail.error || detail.body}
                        </p>
                    </div>
                )}
            </PopoverContent>
        </Popover>
    );
}

function APIKeyKeyItem({
    apiKey,
    statsLayoutId,
    editLayoutId,
    deleteLayoutId,
    onViewStats,
    onEdit,
    onDelete,
    isDeleting,
}: {
    apiKey: APIKey;
    statsLayoutId: string;
    editLayoutId: string;
    deleteLayoutId: string;
    onViewStats: () => void;
    onEdit: () => void;
    onDelete: () => void;
    isDeleting: boolean;
}) {
    const t = useTranslations('setting');
    const [confirmDelete, setConfirmDelete] = useState(false);

    return (
        <motion.div
            layout
            exit={{ opacity: 0, scale: 0.95, transition: { duration: 0.2 } }}
            transition={{ type: 'spring', stiffness: 500, damping: 30 }}
            className="group relative flex items-center justify-between gap-3 p-3 rounded-xl bg-muted/50 overflow-hidden origin-top"
        >
            <span className="text-sm font-medium truncate">{apiKey.name}</span>

            <div className="flex items-center gap-1.5">
                <APIKeyTestPopover apiKey={apiKey} />
                <motion.button
                    type="button"
                    layoutId={statsLayoutId}
                    onClick={onViewStats}
                    className="flex size-8 items-center justify-center rounded-lg bg-muted/60 text-muted-foreground transition-colors hover:bg-muted hover:text-foreground active:scale-95"
                >
                    <Info className="size-4" />
                </motion.button>
                <motion.button
                    type="button"
                    layoutId={editLayoutId}
                    onClick={onEdit}
                    className="flex size-8 items-center justify-center rounded-lg bg-muted/60 text-muted-foreground transition-colors hover:bg-muted hover:text-foreground active:scale-95"
                >
                    <Pencil className="size-4" />
                </motion.button>
                <CopyIconButton
                    text={apiKey.api_key}
                    className="flex size-8 items-center justify-center rounded-lg bg-primary/10 text-primary transition-all hover:bg-primary hover:text-primary-foreground active:scale-95"
                    copyIconClassName="size-4"
                    checkIconClassName="size-4"
                />

                {!confirmDelete && (
                    <motion.button
                        layoutId={deleteLayoutId}
                        onClick={() => setConfirmDelete(true)}
                        className="flex size-8 items-center justify-center rounded-lg bg-destructive/10 text-destructive transition-colors hover:bg-destructive hover:text-destructive-foreground"
                    >
                        <Trash2 className="size-4" />
                    </motion.button>
                )}
            </div>

            <AnimatePresence>
                {confirmDelete && (
                    <motion.div
                        layoutId={deleteLayoutId}
                        className="absolute inset-0 flex items-center justify-center gap-2 bg-destructive p-3 rounded-xl"
                        transition={{ type: 'spring', stiffness: 400, damping: 30 }}
                    >
                        <button
                            onClick={() => setConfirmDelete(false)}
                            className="flex size-8 items-center justify-center rounded-lg bg-destructive-foreground/20 text-destructive-foreground transition-all hover:bg-destructive-foreground/30 active:scale-95"
                        >
                            <X className="size-4" />
                        </button>
                        <button
                            onClick={onDelete}
                            disabled={isDeleting}
                            className="flex-1 h-8 flex items-center justify-center gap-1.5 rounded-lg bg-destructive-foreground text-destructive text-sm font-medium transition-all hover:bg-destructive-foreground/90 active:scale-[0.98] disabled:opacity-50"
                        >
                            <Trash2 className="size-3.5" />
                            {isDeleting ? '...' : t('apiKey.form.confirm')}
                        </button>
                    </motion.div>
                )}
            </AnimatePresence>
        </motion.div>
    );
}

function APIKeyPanelBase({
    idPrefix,
    containerClassName,
    listClassName,
    renderHeaderExtra,
}: {
    idPrefix: string;
    containerClassName: string;
    listClassName: string;
    renderHeaderExtra?: (ctx: {
        disabled: boolean;
    }) => React.ReactNode;
}) {
    const t = useTranslations('setting');
    const { data: apiKeys, isLoading: apiKeysLoading, error: apiKeysError } = useAPIKeyList();
    const createAPIKey = useCreateAPIKey();
    const updateAPIKey = useUpdateAPIKey();
    const deleteAPIKey = useDeleteAPIKey();

    const instanceId = useId();
    const addLayoutId = `add-btn-${idPrefix}-${instanceId}`;
    const statsPrefix = `${idPrefix}-stats-${instanceId}`;
    const editPrefix = `${idPrefix}-edit-${instanceId}`;
    const deletePrefix = `${idPrefix}-delete-`;

    const [isAdding, setIsAdding] = useState(false);
    const [viewingStats, setViewingStats] = useState<APIKey | null>(null);
    const [editingKey, setEditingKey] = useState<APIKey | null>(null);
    const [deletingId, setDeletingId] = useState<number | null>(null);

    const apiKeyList = apiKeys ?? [];

    const handleDelete = (id: number) => {
        setDeletingId(id);
        deleteAPIKey.mutate(id, {
            onSuccess: () => {
                toast.success(t('apiKey.toast.deleteSuccess'));
            },
            onError: (error) => {
                const msg = error instanceof Error ? error.message : undefined;
                toast.error(t('apiKey.toast.deleteError'), { description: msg });
            },
            onSettled: () => setDeletingId((cur) => (cur === id ? null : cur)),
        });
    };

    const closeAllOverlays = () => {
        setIsAdding(false);
        setViewingStats(null);
        setEditingKey(null);
    };

    const disabledHeaderActions = createAPIKey.isPending || isAdding || !!viewingStats || !!editingKey;

    const handleCreate = (data: APIKeyFormData) => {
        createAPIKey.mutate(data, {
            onSuccess: () => {
                toast.success(t('apiKey.toast.createSuccess'));
                setIsAdding(false);
            },
            onError: (error) => {
                const msg = error instanceof Error ? error.message : undefined;
                toast.error(t('apiKey.toast.createError'), { description: msg });
            },
        });
    };

    const handleUpdate = (apiKey: APIKey, data: APIKeyFormData) => {
        updateAPIKey.mutate({ id: apiKey.id, ...data }, {
            onSuccess: () => {
                toast.success(t('apiKey.toast.updateSuccess'));
                setEditingKey(null);
            },
            onError: (error) => {
                const msg = error instanceof Error ? error.message : undefined;
                toast.error(t('apiKey.toast.updateError'), { description: msg });
            },
        });
    };

    return (
        <div className={containerClassName}>
            <div className="flex items-center justify-between gap-3">
                <h2 className="text-lg font-bold text-card-foreground flex items-center gap-2">
                    <KeyRound className="h-5 w-5" />
                    {t('apiKey.title')}
                </h2>
                <div className="flex items-center gap-2">
                    <motion.button
                        layoutId={addLayoutId}
                        type="button"
                        onClick={() => setIsAdding(true)}
                        disabled={disabledHeaderActions}
                        className="h-9 w-9 flex items-center justify-center rounded-lg bg-muted/60 text-muted-foreground transition-colors hover:bg-muted disabled:opacity-50"
                    >
                        <Plus className="size-4" />
                    </motion.button>
                    {renderHeaderExtra?.({ disabled: disabledHeaderActions })}
                </div>
            </div>

            <AnimatePresence>
                {isAdding && (
                    <APIKeyFormOverlay
                        layoutId={addLayoutId}
                        isPending={createAPIKey.isPending}
                        submitLabel={t('apiKey.form.create')}
                        onSubmit={handleCreate}
                        onClose={() => setIsAdding(false)}
                    />
                )}
            </AnimatePresence>

            <AnimatePresence>
                {viewingStats && (
                    <APIKeyStatsCard
                        layoutId={`${statsPrefix}-${viewingStats.id}`}
                        apiKey={viewingStats}
                        onClose={() => setViewingStats(null)}
                    />
                )}
            </AnimatePresence>

            <AnimatePresence>
                {editingKey && (
                    <APIKeyFormOverlay
                        layoutId={`${editPrefix}-${editingKey.id}`}
                        apiKey={editingKey}
                        isPending={updateAPIKey.isPending}
                        submitLabel={t('apiKey.form.save')}
                        onSubmit={(data) => handleUpdate(editingKey, data)}
                        onClose={() => setEditingKey(null)}
                    />
                )}
            </AnimatePresence>

            <div className={listClassName}>
                {apiKeysLoading ? (
                    <div className="h-full flex items-center justify-center text-sm text-muted-foreground">
                        <Loader className="size-4 animate-spin" />
                    </div>
                ) : apiKeysError ? (
                    <div className="h-full flex items-center justify-center text-sm text-destructive">
                        {t('apiKey.loadFailed')}
                    </div>
                ) : apiKeys?.length === 0 ? (
                    <div className="h-full flex items-center justify-center text-sm text-muted-foreground">
                        {t('apiKey.empty')}
                    </div>
                ) : (
                    <AnimatePresence>
                        {apiKeyList.map((apiKey) => {
                            const statsLayoutId = `${statsPrefix}-${apiKey.id}`;
                            const editLayoutId = `${editPrefix}-${apiKey.id}`;
                            const deleteLayoutId = `${deletePrefix}${apiKey.id}`;
                            return (
                                <APIKeyKeyItem
                                    key={apiKey.id}
                                    apiKey={apiKey}
                                    statsLayoutId={statsLayoutId}
                                    editLayoutId={editLayoutId}
                                    deleteLayoutId={deleteLayoutId}
                                    onViewStats={() => {
                                        closeAllOverlays();
                                        setViewingStats(apiKey);
                                    }}
                                    onEdit={() => {
                                        closeAllOverlays();
                                        setEditingKey(apiKey);
                                    }}
                                    onDelete={() => handleDelete(apiKey.id)}
                                    isDeleting={deleteAPIKey.isPending && deletingId === apiKey.id}
                                />
                            );
                        })}
                    </AnimatePresence>
                )}
            </div>
        </div>
    );
}

function APIKeyDialogPanel() {
    const { setIsOpen } = useMorphingDialog();
    return (
        <APIKeyPanelBase
            idPrefix="apikey-dialog"
            containerClassName="rounded-3xl border border-border bg-card p-6 space-y-5 relative w-screen max-w-full md:max-w-xl"
            listClassName="space-y-2 h-[calc(100vh-10rem)] overflow-y-auto"
            renderHeaderExtra={() => (
                <button
                    type="button"
                    onClick={() => setIsOpen(false)}
                    className="h-9 w-9 flex items-center justify-center rounded-lg bg-muted/60 text-muted-foreground transition-colors hover:bg-muted"
                >
                    <X className="size-4" />
                </button>
            )}
        />
    );
}

export function SettingAPIKey() {
    return (
        <APIKeyPanelBase
            idPrefix="apikey"
            containerClassName="rounded-3xl border border-border bg-card p-6 space-y-5 relative"
            listClassName="space-y-2 h-36 overflow-y-auto"
            renderHeaderExtra={({ disabled }) => (
                <MorphingDialog>
                    {disabled ? (
                        <button
                            type="button"
                            disabled
                            className="h-9 w-9 flex items-center justify-center rounded-lg bg-muted/60 text-muted-foreground opacity-50"
                        >
                            <Maximize2 className="size-4" />
                        </button>
                    ) : (
                        <MorphingDialogTrigger className="h-9 w-9 flex items-center justify-center rounded-lg bg-muted/60 text-muted-foreground transition-colors hover:bg-muted">
                            <Maximize2 className="size-4" />
                        </MorphingDialogTrigger>
                    )}
                    <MorphingDialogContainer>
                        <MorphingDialogContent className="relative">
                            <APIKeyDialogPanel />
                        </MorphingDialogContent>
                    </MorphingDialogContainer>
                </MorphingDialog>
            )}
        />
    );
}
