import { existsSync } from 'node:fs'
import { readFile } from 'node:fs/promises'
import { join, resolve } from 'node:path'
import { z } from 'zod'
import { type ScribaConfig, scribaConfigSchema } from './schema.ts'

export type LoadConfigOptions = {
	cwd?: string
	configPath?: string
	env?: Record<string, string | undefined>
}

export type LoadedConfig = {
	config: ScribaConfig
	path: string | null
	warnings: string[]
}

function defaultUserConfigPath(env: Record<string, string | undefined>): string | null {
	const xdgConfigHome = env.XDG_CONFIG_HOME
	const home = env.HOME
	if (xdgConfigHome != null && xdgConfigHome.trim() !== '') {
		return join(xdgConfigHome, 'scriba', 'config.json')
	}
	if (home != null && home.trim() !== '') {
		return join(home, '.config', 'scriba', 'config.json')
	}
	return null
}

export function discoverConfigPaths(options: LoadConfigOptions = {}): string[] {
	const cwd = resolve(options.cwd ?? process.cwd())
	const env = options.env ?? process.env
	const paths: string[] = []

	if (options.configPath != null && options.configPath.trim() !== '') {
		paths.push(resolve(cwd, options.configPath))
		return paths
	}

	paths.push(join(cwd, '.scriba', 'config.json'))
	const userPath = defaultUserConfigPath(env)
	if (userPath != null) {
		paths.push(userPath)
	}
	return paths
}

export async function loadConfig(options: LoadConfigOptions = {}): Promise<LoadedConfig> {
	const warnings: string[] = []
	for (const configPath of discoverConfigPaths(options)) {
		if (!existsSync(configPath)) {
			continue
		}

		const raw = await readFile(configPath, 'utf8')
		const parsed = JSON.parse(raw) as unknown
		const result = scribaConfigSchema.safeParse(parsed)
		if (!result.success) {
			throw new Error(z.prettifyError(result.error))
		}
		return { config: result.data, path: configPath, warnings }
	}

	return { config: scribaConfigSchema.parse({}), path: null, warnings }
}
