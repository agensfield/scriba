import { homedir } from 'node:os'
import { join, relative, resolve } from 'node:path'
import { z } from 'zod'
import { fileSize, isDirectory, walkJsonlFiles } from './files.ts'
import { readJsonlLines } from './jsonl.ts'
import { emptyScannerStats, type LocalUsageEvent, type ScanResult } from './types.ts'

const claudeUsageSchema = z.object({
	cwd: z.string().optional(),
	sessionId: z.string().optional(),
	timestamp: z.string(),
	version: z.string().optional(),
	requestId: z.string().optional(),
	costUSD: z.number().optional(),
	isApiErrorMessage: z.boolean().optional(),
	message: z.object({
		id: z.string().optional(),
		model: z.string().optional(),
		usage: z.object({
			input_tokens: z.number().default(0),
			output_tokens: z.number().default(0),
			cache_creation_input_tokens: z.number().default(0),
			cache_read_input_tokens: z.number().default(0),
		}),
	}),
})

export type ClaudeScanOptions = {
	paths?: string[] | undefined
	env?: Record<string, string | undefined> | undefined
}

export function defaultClaudeProjectDirs(env: Record<string, string | undefined> = process.env) {
	const configured = env.CLAUDE_CONFIG_DIR?.trim()
	const baseDirs =
		configured != null && configured !== ''
			? configured
					.split(',')
					.map((value) => value.trim())
					.filter(Boolean)
			: [join(homedir(), '.config', 'claude'), join(homedir(), '.claude')]

	return baseDirs.map((baseDir) => join(baseDir, 'projects'))
}

function projectFromPath(projectsDir: string, filePath: string): string | undefined {
	const parts = relative(projectsDir, filePath).split(/[\\/]/)
	return parts[0] === '' ? undefined : parts[0]
}

function sessionFromPath(projectsDir: string, filePath: string): string {
	const rel = relative(projectsDir, filePath)
	return rel
		.replace(/\.jsonl$/i, '')
		.split(/[\\/]/)
		.join('/')
}

export async function scanClaudeLogs(options: ClaudeScanOptions = {}): Promise<ScanResult> {
	const stats = emptyScannerStats()
	const events: LocalUsageEvent[] = []
	const seen = new Set<string>()
	const dirs = options.paths?.map((path) => resolve(path)) ?? defaultClaudeProjectDirs(options.env)

	for (const dir of dirs) {
		if (!(await isDirectory(dir))) {
			stats.missingDirectories.push(dir)
			continue
		}

		for await (const filePath of walkJsonlFiles(dir)) {
			stats.files += 1
			stats.bytes += await fileSize(filePath)
			for await (const { line } of readJsonlLines(filePath)) {
				stats.lines += 1
				let parsed: unknown
				try {
					parsed = JSON.parse(line)
				} catch {
					stats.invalidLines += 1
					continue
				}

				const result = claudeUsageSchema.safeParse(parsed)
				if (!result.success) {
					continue
				}

				const data = result.data
				const uniqueKey =
					data.message.id != null && data.requestId != null
						? `${data.message.id}:${data.requestId}`
						: undefined
				if (uniqueKey != null && seen.has(uniqueKey)) {
					stats.duplicates += 1
					continue
				}
				if (uniqueKey != null) {
					seen.add(uniqueKey)
				}

				const usage = data.message.usage
				const inputTokens = usage.input_tokens
				const outputTokens = usage.output_tokens
				const cacheCreationTokens = usage.cache_creation_input_tokens
				const cacheReadTokens = usage.cache_read_input_tokens

				events.push({
					providerId: 'claude',
					sessionId: data.sessionId ?? sessionFromPath(dir, filePath),
					timestamp: data.timestamp,
					model: data.message.model ?? 'unknown',
					project: projectFromPath(dir, filePath),
					projectPath: data.cwd,
					inputTokens,
					outputTokens,
					cacheCreationTokens,
					cacheReadTokens,
					cachedInputTokens: cacheReadTokens,
					reasoningOutputTokens: 0,
					totalTokens: inputTokens + outputTokens + cacheCreationTokens + cacheReadTokens,
					costUSD: data.costUSD ?? null,
					uniqueKey,
					sourcePath: filePath,
				})
				stats.events += 1
			}
		}
	}

	return { events, stats }
}
