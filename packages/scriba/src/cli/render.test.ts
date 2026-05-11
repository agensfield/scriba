import { describe, expect, it } from 'vitest'
import { SCHEMA_VERSION } from '../schema/model.ts'
import { renderReport, renderStatus } from './render.ts'

describe('human renderers', () => {
	it('renders status output with high-contrast text and readable progress', () => {
		const rendered = renderStatus({
			schemaVersion: SCHEMA_VERSION,
			generatedAt: '2026-05-05T13:00:00.000Z',
			providers: [
				{
					providerId: 'codex',
					displayName: 'Codex',
					state: 'ok',
					provenance: [],
					lines: [
						{ type: 'badge', label: 'Plan', text: 'prolite' },
						{
							type: 'progress',
							label: '5h limit',
							used: 18,
							limit: 100,
							format: { kind: 'percent' },
							resetsAt: '2099-05-05T18:00:00.000Z',
						},
						{ type: 'text', label: 'Today', value: '23,135,924' },
					],
				},
			],
		})

		expect(rendered).toContain('Codex')
		expect(rendered).toContain('5h limit')
		expect(rendered).toContain('18%')
		expect(rendered).toContain('resets in')
		expect(rendered).not.toContain('used 18% of 100%')
		expect(rendered).toContain('▰▰▰▰')
		expect(rendered).not.toContain('░')
	})

	it('renders report tables without murky placeholder glyphs', () => {
		const rendered = renderReport('Codex Daily', {
			providerId: 'codex',
			stats: {
				files: 2,
				bytes: 1024,
				lines: 4,
				events: 1,
				invalidLines: 0,
				duplicates: 0,
				missingDirectories: [],
			},
			rows: [
				{
					date: '2026-05-05',
					totalTokens: 1200,
					inputTokens: 1000,
					outputTokens: 200,
					cacheReadTokens: 100,
					costUSD: 0.1234,
				},
			],
		})

		expect(rendered).toContain('Codex Daily')
		expect(rendered).toContain('bucket')
		expect(rendered).toContain('2026-05-05')
		expect(rendered).not.toContain('░')
	})
})
