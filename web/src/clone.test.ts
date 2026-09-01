import { reactive } from 'vue'
import { describe, expect, it } from 'vitest'
import { clone } from './clone'

describe('clone', () => {
  it('copies Vue reactive API records without a DataCloneError', () => {
    const source = reactive({ id: 'node-1', nested: { tags: ['edge'] } })
    const copy = clone(source)
    expect(copy).toEqual({ id: 'node-1', nested: { tags: ['edge'] } })
    copy.nested.tags.push('changed')
    expect(source.nested.tags).toEqual(['edge'])
  })
})
