import { writeFile } from 'node:fs/promises'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'
import { withTempDir } from '../test/temp.ts'
import { claudeKeychainServices, probeClaudeUsage } from './claude.ts'
import { probeCodexUsage } from './codex.ts'

describe('remote provider probes', () => {
	it('maps Claude usage windows to metric lines', async () => {
		await withTempDir('scriba-claude-auth-', async (root) => {
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
				now: new Date('2026-05-05T13:00:00.000Z'),
				fetch: async () =>
					Response.json({
						five_hour: { utilization: 12, resets_at: '2026-05-05T12:00:00.000Z' },
						seven_day: { utilization: 34, resets_at: '2026-05-06T12:00:00.000Z' },
						seven_day_oauth_apps: { utilization: 8 },
						seven_day_sonnet: { utilization: 56 },
						seven_day_design: { utilization: 78 },
						seven_day_routines: { utilization: 9 },
						extra_usage: { is_enabled: true, used_credits: 250, monthly_limit: 1000 },
					}),
			})
			expect(result.lines.map((line) => line.label)).toEqual([
				'Peak Hours',
				'5h limit',
				'Weekly limit',
				'OAuth Apps',
				'Sonnet',
				'Claude Design',
				'Claude Routines',
				'Extra usage spent',
			])
		})
	})

	it('loads Claude OAuth credentials from macOS keychain fallback', async () => {
		const credentialJson = JSON.stringify({
			claudeAiOauth: {
				accessToken: 'keychain-token',
				refreshToken: 'refresh',
				expiresAt: Date.now() + 60 * 60 * 1000,
			},
		})
		const seenHeaders: string[] = []
		const result = await probeClaudeUsage({
			credentialPaths: ['/tmp/scriba-missing-claude-credentials.json'],
			keychainServices: ['Claude Code-credentials'],
			readKeychainCredential: async (service) =>
				service === 'Claude Code-credentials' ? credentialJson : null,
			fetch: async (_input, init) => {
				seenHeaders.push(new Headers(init?.headers).get('authorization') ?? '')
				return Response.json({ five_hour: { utilization: 1 } })
			},
		})

		expect(result.authState).toMatchObject({
			ok: true,
			source: 'keychain:Claude Code-credentials',
		})
		expect(seenHeaders).toEqual(['Bearer keychain-token'])
		expect(result.lines.find((line) => line.label === '5h limit')).toBeDefined()
	})

	it('accepts hex-encoded Claude keychain credentials', async () => {
		const credentialJson = JSON.stringify({
			claudeAiOauth: {
				accessToken: 'hex-token',
				expiresAt: Date.now() + 60 * 60 * 1000,
			},
		})
		const result = await probeClaudeUsage({
			credentialPaths: ['/tmp/scriba-missing-claude-credentials.json'],
			keychainServices: ['Claude Code-credentials'],
			readKeychainCredential: async () =>
				`0x${Buffer.from(credentialJson, 'utf8').toString('hex')}`,
			fetch: async () => Response.json({ five_hour: { utilization: 1 } }),
		})

		expect(result.authState.ok).toBe(true)
		expect(result.lines.find((line) => line.label === '5h limit')).toBeDefined()
	})

	it('uses Claude config-dir hashed keychain service before legacy service', () => {
		expect(claudeKeychainServices({ CLAUDE_CONFIG_DIR: '/Users/test/.claude' })).toEqual([
			'Claude Code-credentials-462977e4',
			'Claude Code-credentials',
		])
	})

	it('maps Codex usage windows to metric lines', async () => {
		await withTempDir('scriba-codex-auth-', async (root) => {
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
							secondary_window: {
								used_percent: 15,
								reset_at: 1_778_000_000,
								limit_window_seconds: 604800,
							},
						},
						credits: { has_credits: true, unlimited: false, balance: '4.2' },
						plan_type: 'pro',
					}),
			})
			expect(result.lines.map((line) => line.label)).toEqual([
				'Plan',
				'5h limit',
				'Weekly limit',
				'Spark weekly',
				'Spark 5h',
				'Review 5h',
				'Review weekly',
				'Credits left',
			])
			expect(result.lines.find((line) => line.label === 'Credits left')).toMatchObject({
				type: 'amount',
				value: 4.2,
			})
		})
	})
})
