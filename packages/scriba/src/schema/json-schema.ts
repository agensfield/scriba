import { z } from 'zod'
import { SCHEMA_VERSION, schemaRegistry } from './model.ts'

export function buildJsonSchemaRegistry() {
	return {
		schemaVersion: SCHEMA_VERSION,
		generatedAt: new Date().toISOString(),
		schemas: Object.fromEntries(
			Object.entries(schemaRegistry).map(([name, schema]) => [name, z.toJSONSchema(schema)]),
		),
	}
}
