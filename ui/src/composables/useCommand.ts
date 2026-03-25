import { ref } from 'vue'
import { runCommand, shellQuote, type CmdResult } from '../api/client'

export function useCommand() {
  const loading = ref(false)
  const error = ref('')
  const output = ref('')
  const success = ref(false)

  async function execute(command: string): Promise<CmdResult> {
    loading.value = true
    error.value = ''
    output.value = ''
    success.value = false

    try {
      const result = await runCommand(command)
      if (result.success) {
        output.value = result.output || ''
        success.value = true
      } else {
        error.value = result.output || result.error || 'Command failed'
      }
      return result
    } catch (err: any) {
      error.value = err.message || 'Network error'
      return { success: false, error: error.value }
    } finally {
      loading.value = false
    }
  }

  function reset() {
    loading.value = false
    error.value = ''
    output.value = ''
    success.value = false
  }

  return { loading, error, output, success, execute, reset, shellQuote }
}
