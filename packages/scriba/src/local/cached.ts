import { resolve } from 'node:path'
import type { ScribaCache } from '../cache/sqlite.ts'
import type { ProviderId } from '../schema/model.ts'
import { addClaudeParsedFileStats, defaultClaudeProjectDirs, parseClaudeFile } from './claude.ts'
import { defaultCodexSessionDirs, parseCodexFile } from './codex.ts'
import { fileFingerprint, isDirectory, walkJsonlFiles } from './files.ts'
import {
	addScannerStats,
	emptyScannerStats,
	type LocalUsageEvent,
	type ScannerStats,
} from './types.ts'

export type CachedCodexEventOptions = {
	cache: ScribaCache
	paths?: string[] | undefined
	env?: Record<string, string | undefined> | undefined
	stats?: ScannerStats | undefined
}

export type CachedClaudeEventOptions = CachedCodexEventOptions

const CLAUDE_PROVIDER_ID: ProviderId = 'claude'
const CODEX_PROVIDER_ID: ProviderId = 'codex'

export async function* iterateCachedClaudeEvents(
	options: CachedClaudeEventOptions,
): AsyncGenerator<LocalUsageEvent> {
	const stats = options.stats ?? emptyScannerStats()
	const seen = new Set<string>()
	const dirs = options.paths?.map((path) => resolve(path)) ?? defaultClaudeProjectDirs(options.env)

	for (const dir of dirs) {
		if (!(await isDirectory(dir))) {
			stats.missingDirectories.push(dir)
			continue
		}

		for await (const filePath of walkJsonlFiles(dir)) {
			const fingerprint = await fileFingerprint(filePath)
			const cached = options.cache.loadFileEvents<LocalUsageEvent>(
				CLAUDE_PROVIDER_ID,
				filePath,
				fingerprint,
			)
			const parsed = cached ?? (await parseClaudeFile(dir, filePath))
			if (cached == null) {
				options.cache.saveFileEvents(
					CLAUDE_PROVIDER_ID,
					filePath,
					fingerprint,
					parsed.events,
					parsed.stats,
				)
			}
			addClaudeParsedFileStats(stats, parsed.stats)
			for (const event of parsed.events) {
				if (event.uniqueKey != null && seen.has(event.uniqueKey)) {
					stats.duplicates += 1
					continue
				}
				if (event.uniqueKey != null) {
					seen.add(event.uniqueKey)
				}
				stats.events += 1
				yield event
			}
		}
	}
}

export async function* iterateCachedCodexEvents(
	options: CachedCodexEventOptions,
): AsyncGenerator<LocalUsageEvent> {
	const stats = options.stats ?? emptyScannerStats()
	const dirs = options.paths?.map((path) => resolve(path)) ?? defaultCodexSessionDirs(options.env)

	for (const dir of dirs) {
		if (!(await isDirectory(dir))) {
			stats.missingDirectories.push(dir)
			continue
		}

		for await (const filePath of walkJsonlFiles(dir)) {
			const fingerprint = await fileFingerprint(filePath)
			const cached = options.cache.loadFileEvents<LocalUsageEvent>(
				CODEX_PROVIDER_ID,
				filePath,
				fingerprint,
			)
			const parsed = cached ?? (await parseCodexFile(dir, filePath))
			if (cached == null) {
				options.cache.saveFileEvents(
					CODEX_PROVIDER_ID,
					filePath,
					fingerprint,
					parsed.events,
					parsed.stats,
				)
			}
			addScannerStats(stats, parsed.stats)
			for (const event of parsed.events) {
				yield event
			}
		}
	}
}
