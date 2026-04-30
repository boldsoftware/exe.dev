import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import CommandModal from '../components/CommandModal.vue'
import { runCommand } from '../api/client'

vi.mock('../api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api/client')>()
  return {
    ...actual,
    runCommand: vi.fn(),
  }
})

const mockRunCommand = vi.mocked(runCommand)

describe('CommandModal', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mockRunCommand.mockResolvedValue({ success: true, output: 'ok' })
  })

  it('splits text input into separate command args when requested', async () => {
    const wrapper = mount(CommandModal, {
      props: {
        visible: true,
        title: 'Add Tags',
        commandPrefix: 'tag my-vm',
        inputPlaceholder: 'tag names',
        splitInputArgs: true,
      },
    })

    await wrapper.find('input').setValue('prod, web qa')
    await wrapper.findAll('button').find(btn => btn.text() === 'Run')!.trigger('click')
    await flushPromises()

    expect(mockRunCommand).toHaveBeenCalledWith('tag my-vm prod web qa')
  })

  it('keeps text input as one quoted arg by default', async () => {
    const wrapper = mount(CommandModal, {
      props: {
        visible: true,
        title: 'Comment',
        commandPrefix: 'comment my-vm',
        inputPlaceholder: 'comment',
      },
    })

    await wrapper.find('input').setValue('hello world')
    await wrapper.findAll('button').find(btn => btn.text() === 'Run')!.trigger('click')
    await flushPromises()

    expect(mockRunCommand).toHaveBeenCalledWith("comment my-vm 'hello world'")
  })
})
