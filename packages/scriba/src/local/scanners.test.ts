import { mkdir, mkdtemp, writeFile } from 'node:fs/promises'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'
import { scanClaudeLogs } from './claude.ts'
import { scanCodexLogs } from './codex.ts'

describe('local scanners', () => {
	it('streams Claude JSONL and dedupes message/request pairs', async () => {
		const root = await mkdtemp('/tmp/scriba-claude-')
		const projectDir = join(root, 'projects', 'proj-a', 'session-a')
		await mkdir(projectDir, { recursive: true })
		const entry = {
			timestamp: '2026-05-05T10:00:00.000Z',
			sessionId: 'session-a',
			requestId: 'req-1',
			costUSD: 0.12,
			message: {
				id: 'msg-1',
				model: 'claude-sonnet-4-5',
				usage: {
					input_tokens: 100,
					output_tokens: 20,
					cache_creation_input_tokens: 10,
					cache_read_input_tokens: 5,
				},
			},
		}
		await writeFile(
			join(projectDir, 'usage.jsonl'),
			`${JSON.stringify(entry)}\n${JSON.stringify(entry)}\nnot-json\n`,
		)

		const result = await scanClaudeLogs({ paths: [join(root, 'projects')] })
		expect(result.stats.files).toBe(1)
		expect(result.stats.lines).toBe(3)
		expect(result.stats.invalidLines).toBe(1)
		expect(result.stats.duplicates).toBe(1)
		expect(result.events).toHaveLength(1)
		expect(result.events[0]?.totalTokens).toBe(135)
		expect(result.events[0]?.costUSD).toBe(0.12)
	})

	it('streams Codex JSONL and converts cumulative totals into deltas', async () => {
		const root = await mkdtemp('/tmp/scriba-codex-')
		const sessionsDir = join(root, 'sessions')
		await mkdir(sessionsDir, { recursive: true })
		await writeFile(
			join(sessionsDir, 'project-a.jsonl'),
			[
				JSON.stringify({
					type: 'turn_context',
					payload: { model: 'gpt-5.3-codex' },
				}),
				JSON.stringify({
					type: 'event_msg',
					timestamp: '2026-05-05T10:00:00.000Z',
					payload: {
						type: 'token_count',
						info: {
							total_token_usage: {
								input_tokens: 100,
								cached_input_tokens: 10,
								output_tokens: 20,
								reasoning_output_tokens: 5,
								total_tokens: 120,
							},
						},
					},
				}),
				JSON.stringify({
					type: 'event_msg',
					timestamp: '2026-05-05T10:01:00.000Z',
					payload: {
						type: 'token_count',
						info: {
							total_token_usage: {
								input_tokens: 140,
								cached_input_tokens: 15,
								output_tokens: 30,
								reasoning_output_tokens: 7,
								total_tokens: 170,
							},
						},
					},
				}),
			].join('\n'),
		)

		const result = await scanCodexLogs({ paths: [sessionsDir] })
		expect(result.events).toHaveLength(2)
		expect(result.events[0]?.inputTokens).toBe(100)
		expect(result.events[1]?.inputTokens).toBe(40)
		expect(result.events[1]?.totalTokens).toBe(50)
		expect(result.events[1]?.model).toBe('gpt-5.3-codex')
	})
})
