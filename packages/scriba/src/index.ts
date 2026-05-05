export * from './bench/index.ts'
export * from './cache/index.ts'
export { CLI_COMMANDS, createRootCommand } from './cli/command.ts'
export * from './config/index.ts'
export { SCRIBA_PACKAGE_NAME } from './constants.ts'
export * from './doctor/index.ts'
export * from './local/index.ts'
export * from './privacy/index.ts'
export * from './providers/index.ts'
export * from './remote/index.ts'
export * from './reports/index.ts'
export * from './schema/index.ts'
export * from './status/index.ts'
export * from './telegram/index.ts'
export { VERSION } from './version.ts'

export type ProviderId = 'claude' | 'codex' | (string & {})

export type SourceKind = 'local-log' | 'provider-api' | 'cache'

export type SourceProvenance = {
	kind: SourceKind
	providerId: ProviderId
	fetchedAt?: string
	cacheAgeMs?: number
	stale?: boolean
	error?: string
}

export type MetricFormat =
	| { kind: 'percent' }
	| { kind: 'dollars' }
	| { kind: 'count'; suffix: string }

export type MetricLine =
	| { type: 'text'; label: string; value: string; provenance?: SourceProvenance[] }
	| {
			type: 'amount'
			label: string
			value: number
			format: MetricFormat
			provenance?: SourceProvenance[]
	  }
	| {
			type: 'progress'
			label: string
			used: number
			limit: number
			format: MetricFormat
			resetsAt?: string
			periodDurationMs?: number
			provenance?: SourceProvenance[]
	  }
	| { type: 'badge'; label: string; text: string; provenance?: SourceProvenance[] }

export type ProviderSnapshot = {
	providerId: ProviderId
	displayName: string
	plan?: string
	lines: MetricLine[]
	provenance: SourceProvenance[]
}

export type StatusSnapshot = {
	generatedAt: string
	providers: ProviderSnapshot[]
}

export type ProviderAdapter = {
	id: ProviderId
	displayName: string
	buildStatusSnapshot(): Promise<ProviderSnapshot>
}
