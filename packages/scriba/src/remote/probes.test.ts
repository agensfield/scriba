import { mkdtemp, writeFile } from 'node:fs/promises'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'
import { probeClaudeUsage } from './claude.ts'
import { probeCodexUsage } from './codex.ts'

describe('remote provider probes', () => {
	it('maps Claude usage windows to metric lines', async () => {
		const root = await mkdtemp('/tmp/scriba-claude-auth-')
		const credentialPath = join(root, '.credentials.json')
		await writeFile(
			credentialPath,
			JSON.stringify({
				claudeAiOauth: {
					accessToken: 'token',
					refreshToken: 'refresh',
					expiresAt: Date.now() + 60 * 60 * 1000,
				},
			}),
		)
		const result = await probeClaudeUsage({
			credentialPaths: [credentialPath],
			fetch: async () =>
				Response.json({
					five_hour: { utilization: 12, resets_at: '2026-05-05T12:00:00.000Z' },
					seven_day: { utilization: 34, resets_at: '2026-05-06T12:00:00.000Z' },
					extra_usage: { is_enabled: true, used_credits: 250, monthly_limit: 1000 },
				}),
		})
		expect(result.lines.map((line) => line.label)).toEqual([
			'Session',
			'Weekly',
			'Extra usage spent',
		])
	})

	it('maps Codex usage windows to metric lines', async () => {
		const root = await mkdtemp('/tmp/scriba-codex-auth-')
		const authPath = join(root, 'auth.json')
		await writeFile(
			authPath,
			JSON.stringify({
				tokens: {
					access_token: 'token',
					refresh_token: 'refresh',
					account_id: 'acct',
				},
				last_refresh: new Date().toISOString(),
			}),
		)
		const result = await probeCodexUsage({
			authPaths: [authPath],
			fetch: async () =>
				Response.json({
					rate_limit: {
						primary_window: {
							used_percent: 10,
							reset_at: 1_777_936_402,
							limit_window_seconds: 18000,
						},
						secondary_window: {
							used_percent: 20,
							reset_at: 1_778_000_000,
							limit_window_seconds: 604800,
						},
					},
					code_review_rate_limit: {
						primary_window: {
							used_percent: 5,
							reset_at: 1_778_000_000,
							limit_window_seconds: 604800,
						},
					},
					credits: { has_credits: true, unlimited: false, balance: 4.2 },
				}),
		})
		expect(result.lines.map((line) => line.label)).toEqual([
			'Session',
			'Weekly',
			'Spark Weekly',
			'Spark',
			'Reviews',
			'Credits',
		])
	})
})
