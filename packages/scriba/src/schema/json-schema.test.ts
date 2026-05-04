import { describe, expect, it } from 'vitest'
import { buildJsonSchemaRegistry } from './json-schema.ts'

describe('JSON schema registry', () => {
	it('exports versioned schemas for agent consumers', () => {
		const registry = buildJsonSchemaRegistry()
		expect(registry.schemaVersion).toBe('scriba.alpha.v1')
		expect(Object.keys(registry.schemas)).toContain('statusSnapshot')
		expect(Object.keys(registry.schemas)).toContain('errorEnvelope')
	})
})
