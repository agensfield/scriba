import { homedir } from 'node:os'
import { basename, dirname, join, relative, resolve } from 'node:path'
import { z } from 'zod'
import { fileSize, isDirectory, walkJsonlFiles } from './files.ts'
import { readJsonlLines } from './jsonl.ts'
import { emptyScannerStats, type LocalUsageEvent, type ScanResult } from './types.ts'

const recordSchema = z.record(z.string(), z.unknown())
const codexEntrySchema = z.object({
	type: z.string(),
	payload: z.unknown().optional(),
	timestamp: z.string().optional(),
})

type RawUsage = {
	inputTokens: number
	cachedInputTokens: number
	outputTokens: number
	reasoningOutputTokens: number
	totalTokens: number
}

export type CodexScanOptions = {
	paths?: string[] | undefined
	env?: Record<string, string | undefined> | undefined
	stats?: ReturnType<typeof emptyScannerStats> | undefined
}

export function defaultCodexSessionDirs(env: Record<string, string | undefined> = process.env) {
	const codexHome = env.CODEX_HOME?.trim() || join(homedir(), '.codex')
	return [join(codexHome, 'sessions')]
}

function numberField(record: Record<string, unknown>, key: string): number {
	const value = record[key]
	return typeof value === 'number' && Number.isFinite(value) ? value : 0
}

function rawUsage(value: unknown): RawUsage | null {
	if (value == null || typeof value !== 'object') {
		return null
	}
	const record = value as Record<string, unknown>
	const inputTokens = numberField(record, 'input_tokens')
	const cachedInputTokens =
		numberField(record, 'cached_input_tokens') || numberField(record, 'cache_read_input_tokens')
	const outputTokens = numberField(record, 'output_tokens')
	const reasoningOutputTokens = numberField(record, 'reasoning_output_tokens')
	const explicitTotal = numberField(record, 'total_tokens')
	return {
		inputTokens,
		cachedInputTokens,
		outputTokens,
		reasoningOutputTokens,
		totalTokens: explicitTotal > 0 ? explicitTotal : inputTokens + outputTokens,
	}
}

function subtractUsage(current: RawUsage, previous: RawUsage | null): RawUsage {
	return {
		inputTokens: Math.max(current.inputTokens - (previous?.inputTokens ?? 0), 0),
		cachedInputTokens: Math.max(current.cachedInputTokens - (previous?.cachedInputTokens ?? 0), 0),
		outputTokens: Math.max(current.outputTokens - (previous?.outputTokens ?? 0), 0),
		reasoningOutputTokens: Math.max(
			current.reasoningOutputTokens - (previous?.reasoningOutputTokens ?? 0),
			0,
		),
		totalTokens: Math.max(current.totalTokens - (previous?.totalTokens ?? 0), 0),
	}
}

function extractModel(value: unknown): string | undefined {
	const parsed = recordSchema.safeParse(value)
	if (!parsed.success) {
		return undefined
	}
	const record = parsed.data
	const direct = [record.model, record.model_name]
	for (const candidate of direct) {
		if (typeof candidate === 'string' && candidate.trim() !== '') {
			return candidate.trim()
		}
	}
	const info = record.info
	if (info != null && typeof info === 'object') {
		return extractModel(info)
	}
	const metadata = record.metadata
	if (metadata != null && typeof metadata === 'object') {
		return extractModel(metadata)
	}
	return undefined
}

export async function scanCodexLogs(options: CodexScanOptions = {}): Promise<ScanResult> {
	const events: LocalUsageEvent[] = []
	const stats = options.stats ?? emptyScannerStats()
	for await (const event of iterateCodexEvents({ ...options, stats })) {
		events.push(event)
	}
	return { events, stats }
}

export async function* iterateCodexEvents(
	options: CodexScanOptions = {},
): AsyncGenerator<LocalUsageEvent> {
	const stats = options.stats ?? emptyScannerStats()
	const dirs = options.paths?.map((path) => resolve(path)) ?? defaultCodexSessionDirs(options.env)

	for (const dir of dirs) {
		if (!(await isDirectory(dir))) {
			stats.missingDirectories.push(dir)
			continue
		}

		for await (const filePath of walkJsonlFiles(dir)) {
			stats.files += 1
			stats.bytes += await fileSize(filePath)
			let previousTotals: RawUsage | null = null
			let currentModel: string | undefined

			for await (const { line } of readJsonlLines(filePath)) {
				stats.lines += 1
				let parsed: unknown
				try {
					parsed = JSON.parse(line)
				} catch {
					stats.invalidLines += 1
					continue
				}

				const entry = codexEntrySchema.safeParse(parsed)
				if (!entry.success) {
					continue
				}

				if (entry.data.type === 'turn_context') {
					currentModel = extractModel(entry.data.payload) ?? currentModel
					continue
				}

				if (entry.data.type !== 'event_msg' || entry.data.timestamp == null) {
					continue
				}

				const payload = recordSchema.safeParse(entry.data.payload)
				if (!payload.success || payload.data.type !== 'token_count') {
					continue
				}
				const info = recordSchema.safeParse(payload.data.info)
				const infoRecord = info.success ? info.data : undefined
				const lastUsage = rawUsage(infoRecord?.last_token_usage)
				const totalUsage = rawUsage(infoRecord?.total_token_usage)
				const usage =
					lastUsage ?? (totalUsage == null ? null : subtractUsage(totalUsage, previousTotals))
				if (totalUsage != null) {
					previousTotals = totalUsage
				}
				if (usage == null || usage.totalTokens === 0) {
					continue
				}

				const extractedModel = extractModel({ ...payload.data, info: infoRecord })
				const model = extractedModel ?? currentModel ?? 'gpt-5'
				currentModel = model
				const relativePath = relative(dir, filePath).split(/[\\/]/).join('/')

				yield {
					providerId: 'codex',
					sessionId: relativePath.replace(/\.jsonl$/i, ''),
					timestamp: entry.data.timestamp,
					model,
					inputTokens: usage.inputTokens,
					outputTokens: usage.outputTokens,
					cacheCreationTokens: 0,
					cacheReadTokens: usage.cachedInputTokens,
					cachedInputTokens: usage.cachedInputTokens,
					reasoningOutputTokens: usage.reasoningOutputTokens,
					totalTokens: usage.totalTokens,
					costUSD: null,
					sourcePath: filePath,
					directory: dirname(relativePath),
					sessionFile: basename(relativePath),
					isFallbackModel: extractedModel == null && currentModel == null,
				}
				stats.events += 1
			}
		}
	}
}
