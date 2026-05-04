import { homedir } from 'node:os'
import { join, resolve } from 'node:path'

export function defaultCacheDir(env: Record<string, string | undefined> = process.env): string {
	const xdgCacheHome = env.XDG_CACHE_HOME?.trim()
	if (xdgCacheHome != null && xdgCacheHome !== '') {
		return join(xdgCacheHome, 'scriba')
	}
	return join(homedir(), '.cache', 'scriba')
}

export function resolveCacheDir(path: string | undefined, env = process.env): string {
	return resolve(path ?? defaultCacheDir(env))
}
