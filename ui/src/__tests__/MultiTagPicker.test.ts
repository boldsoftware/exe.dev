import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import MultiTagPicker from '../components/MultiTagPicker.vue'

describe('MultiTagPicker', () => {
  it('adds tags as space or comma separated tokens are typed', async () => {
    const wrapper = mount(MultiTagPicker, {
      props: {
        modelValue: [],
        tagSummaries: [],
        'onUpdate:modelValue': (value: string[]) => wrapper.setProps({ modelValue: value }),
      },
    })

    await wrapper.find('input').setValue('foo ')
    expect(wrapper.props('modelValue')).toEqual(['foo'])
    expect((wrapper.find('input').element as HTMLInputElement).value).toBe('')

    await wrapper.find('input').setValue('bar,baz ')
    expect(wrapper.props('modelValue')).toEqual(['bar', 'baz', 'foo'])
    expect((wrapper.find('input').element as HTMLInputElement).value).toBe('')
  })

  it('adds multiple selected tags while excluding existing tags', async () => {
    const wrapper = mount(MultiTagPicker, {
      props: {
        modelValue: [],
        excludedTags: ['prod'],
        tagSummaries: [
          { tag: 'prod', integrations: ['github'], more: 0 },
          { tag: 'qa', integrations: [], more: 0 },
          { tag: 'web', integrations: ['proxy'], more: 0 },
        ],
        'onUpdate:modelValue': (value: string[]) => wrapper.setProps({ modelValue: value }),
      },
    })

    expect(wrapper.text()).not.toContain('#prod')
    expect(wrapper.text()).toContain('#qa')
    expect(wrapper.text()).toContain('#web')

    await wrapper.findAll('.tag-option').find(btn => btn.text().includes('#qa'))!.trigger('click')
    await wrapper.findAll('.tag-option').find(btn => btn.text().includes('#web'))!.trigger('click')

    expect(wrapper.props('modelValue')).toEqual(['qa', 'web'])
    expect(wrapper.text()).toContain('#qa ×')
    expect(wrapper.text()).toContain('#web ×')
  })
})
