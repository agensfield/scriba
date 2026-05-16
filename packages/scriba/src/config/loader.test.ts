import { mkdir, writeFile } from 'node:fs/promises'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'
import { withTempDir } from '../test/temp.ts'
import { discoverConfigPaths, loadConfig } from './loader.ts'

describe('config loader', () => {
	it('discovers project config before user config', () => {
		const paths = discoverConfigPaths({
			cwd: '/tmp/project',
			env: { HOME: '/tmp/home' },
		})
		expect(paths).toEqual([
			'/tmp/project/.scriba/config.json',
			'/tmp/home/.config/scriba/config.json',
		])
	})

	it('loads defaults when no config exists', async () => {
		const loaded = await loadConfig({
			cwd: '/tmp/scriba-missing-config',
			env: {},
		})
		expect(loaded.path).toBeNull()
		expect(loaded.config.providers.claude.enabled).toBe(true)
		expect(loaded.config.telegram.enabled).toBe(false)
	})

	it('loads a project config file', async () => {
		await withTempDir('scriba-config-', async (cwd) => {
			await mkdir(join(cwd, '.scriba'), { recursive: true })
			await writeFile(
				join(cwd, '.scriba', 'config.json'),
				JSON.stringify({ locale: 'tr-TR', providers: { codex: { enabled: false } } }),
			)

			const loaded = await loadConfig({ cwd, env: {} })
			expect(loaded.config.locale).toBe('tr-TR')
			expect(loaded.config.providers.codex.enabled).toBe(false)
		})
	})
})
