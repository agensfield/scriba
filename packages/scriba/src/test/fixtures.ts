import { mkdir, writeFile } from 'node:fs/promises'
import { join } from 'node:path'

export async function writeClaudeUsageFixture(root: string) {
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
	return { projectsDir: join(root, 'projects'), entry }
}

export async function writeCodexUsageFixture(root: string) {
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
	return { sessionsDir }
}
