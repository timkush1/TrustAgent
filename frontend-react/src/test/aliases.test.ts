import { describe, expect, it } from 'vitest'
import * as viaAlias from '@/test/aliasProbe'
import * as viaRelative from './aliasProbe'

// `@/*` is declared twice: compilerOptions.paths (tsconfig.app.json, checked by
// `tsc -b`) and resolve.alias (vite.config.ts, used by vite and vitest). This
// test fails to type-check or to import if either side drifts from `src/`.
describe('path aliases', () => {
  it('resolves @/ to the same module as a relative import from src/', () => {
    expect(viaAlias).toBe(viaRelative)
    expect(viaAlias.ALIAS_PROBE).toBe(viaRelative.ALIAS_PROBE)
    expect(viaAlias.ALIAS_PROBE.description).toBe('alias-probe')
  })
})
