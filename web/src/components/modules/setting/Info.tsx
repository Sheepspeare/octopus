import { useTranslations } from 'use-intl';
import { Info, Loader2, Tag } from 'lucide-react';
import Github from '@thesvg/react/github';
import { useNowVersion } from '@/api/update';

const GITHUB_REPO = import.meta.env.VITE_GITHUB_REPO || 'https://github.com/bestruirui/octopus'; // 项目仓库地址。

// SettingInfo 展示版本信息。
// 这里刻意不提供在线更新入口: 当前是自行定制的构建, 官方二进制会覆盖掉全部改动,
// 后端也已在 /api/v1/update 上直接拒绝更新请求。
export function SettingInfo() {
    const t = useTranslations('setting');
    const nowVersionQuery = useNowVersion();

    const backendNowVersion = nowVersionQuery.data || '';

    return (
        <div className="rounded-3xl border border-border bg-card p-6 space-y-5">
            <h2 className="text-lg font-bold text-card-foreground flex items-center gap-2">
                <Info className="h-5 w-5" />
                {t('info.title')}
            </h2>
            {/* GitHub 仓库 */}
            <div className="flex items-center justify-between gap-4">
                <div className="flex items-center gap-3">
                    <Github variant="mono" className="h-5 w-5 text-muted-foreground" />
                    <span className="text-sm font-medium">{t('info.github')}</span>
                </div>
                <a
                    href={GITHUB_REPO}
                    target="_blank"
                    rel="noopener noreferrer"
                    className="text-sm text-primary hover:underline"
                >
                    {GITHUB_REPO.replace('https://github.com/', '')}
                </a>
            </div>
            {/* 当前版本 */}
            <div className="flex items-center justify-between gap-4">
                <div className="flex items-center gap-3">
                    <Tag className="h-5 w-5 text-muted-foreground" />
                    <span className="text-sm font-medium">{t('info.currentVersion')}</span>
                </div>
                <div className="flex items-center gap-2">
                    {nowVersionQuery.isLoading ? (
                        <Loader2 className="size-4 animate-spin text-muted-foreground" />
                    ) : (
                        <code className="text-sm font-mono text-muted-foreground">
                            {backendNowVersion || t('info.unknown')}
                        </code>
                    )}
                </div>
            </div>
            <p className="text-xs text-muted-foreground">{t('info.customBuild')}</p>
        </div>
    );
}
