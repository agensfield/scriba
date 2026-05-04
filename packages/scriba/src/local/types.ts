import type { ProviderId } from '../schema/model.ts'

export type TokenUsage = {
	inputTokens: number
	outputTokens: number
	cacheCreationTokens: number
	cacheReadTokens: number
	cachedInputTokens: number
	reasoningOutputTokens: number
	totalTokens: number
}

export type LocalUsageEvent = TokenUsage & {
	providerId: ProviderId
	sessionId: string
	timestamp: string
	model: string
	project?: string | undefined
	projectPath?: string | undefined
	directory?: string | undefined
	sessionFile?: string | undefined
	costUSD: number | null
	uniqueKey?: string | undefined
	sourcePath: string
	isFallbackModel?: boolean | undefined
}

export type ScannerStats = {
	files: number
	bytes: number
	lines: number
	events: number
	invalidLines: number
	duplicates: number
	missingDirectories: string[]
}

export type ScanResult = {
	events: LocalUsageEvent[]
	stats: ScannerStats
}

export function emptyScannerStats(): ScannerStats {
	return {
		files: 0,
		bytes: 0,
		lines: 0,
		events: 0,
		invalidLines: 0,
		duplicates: 0,
		missingDirectories: [],
	}
}

export function addScannerStats(target: ScannerStats, source: ScannerStats): void {
	target.files += source.files
	target.bytes += source.bytes
	target.lines += source.lines
	target.events += source.events
	target.invalidLines += source.invalidLines
	target.duplicates += source.duplicates
	target.missingDirectories.push(...source.missingDirectories)
}

export function addTokenUsage(target: TokenUsage, event: TokenUsage): void {
	target.inputTokens += event.inputTokens
	target.outputTokens += event.outputTokens
	target.cacheCreationTokens += event.cacheCreationTokens
	target.cacheReadTokens += event.cacheReadTokens
	target.cachedInputTokens += event.cachedInputTokens
	target.reasoningOutputTokens += event.reasoningOutputTokens
	target.totalTokens += event.totalTokens
}

export function emptyTokenUsage(): TokenUsage {
	return {
		inputTokens: 0,
		outputTokens: 0,
		cacheCreationTokens: 0,
		cacheReadTokens: 0,
		cachedInputTokens: 0,
		reasoningOutputTokens: 0,
		totalTokens: 0,
	}
}
