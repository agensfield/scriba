import { resolve } from 'node:path'
import type { ScribaCache } from '../cache/sqlite.ts'
import type { ProviderId } from '../schema/model.ts'
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

const CODEX_PROVIDER_ID: ProviderId = 'codex'

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
