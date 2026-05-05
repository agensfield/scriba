import { defaultClaudeProjectDirs } from '../local/claude.ts'
import { defaultCodexSessionDirs } from '../local/codex.ts'
import { claudeCredentialPaths, probeClaudeUsage } from '../remote/claude.ts'
import { codexAuthPaths, probeCodexUsage } from '../remote/codex.ts'
export type ProviderDescriptor = {
	id: 'claude' | 'codex'
	displayName: string
	defaultLocalPaths(env?: Record<string, string | undefined>): string[]
	authPaths(env?: Record<string, string | undefined>): string[]
	authHint: string
	supportedReports: string[]
	probeUsage: typeof probeClaudeUsage | typeof probeCodexUsage
}

export const PROVIDER_DESCRIPTORS = {
	claude: {
		id: 'claude',
		displayName: 'Claude',
		defaultLocalPaths: defaultClaudeProjectDirs,
		authPaths: claudeCredentialPaths,
		authHint: 'Run `claude` to authenticate.',
		supportedReports: ['summary', 'daily', 'weekly', 'monthly', 'sessions', 'session', 'blocks'],
		probeUsage: probeClaudeUsage,
	},
	codex: {
		id: 'codex',
		displayName: 'Codex',
		defaultLocalPaths: defaultCodexSessionDirs,
		authPaths: codexAuthPaths,
		authHint: 'Run `codex` to authenticate.',
		supportedReports: ['summary', 'daily', 'weekly', 'monthly', 'sessions', 'session'],
		probeUsage: probeCodexUsage,
	},
} as const satisfies Record<'claude' | 'codex', ProviderDescriptor>

export const PROVIDERS = Object.values(PROVIDER_DESCRIPTORS)
