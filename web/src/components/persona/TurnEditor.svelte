<script lang="ts">
  import type { ChatTurn } from '../../api/client'

  interface Props {
    /** The scripted conversation turns of a chat-mode preset. */
    turns: ChatTurn[]
  }

  let { turns = $bindable() }: Props = $props()

  function addTurn(role: 'user' | 'assistant' = 'user') {
    turns = [...turns, { role, content: '' }]
  }

  function removeTurn(index: number) {
    if (turns.length <= 1) {
      turns = [{ role: 'user', content: '' }]
      return
    }
    turns = turns.filter((_, i) => i !== index)
  }
</script>

<!-- Multi-turn Script Editor -->
<div class="space-y-3 rounded-brand-lg border border-border-subtle bg-surface-2/60 p-3.5">
  <div class="flex items-center justify-between">
    <div>
      <span class="text-xs font-semibold text-text-main">Conversation Script Turns</span>
      <p class="text-[11px] text-text-subtle">
        Scripted fake history inserted before real user questions. Roles alternate user ↔ assistant.
      </p>
    </div>
    <button
      type="button"
      onclick={() => addTurn(turns[turns.length - 1]?.role === 'user' ? 'assistant' : 'user')}
      class="flex shrink-0 items-center gap-1 rounded-brand border border-purple-500/30 bg-purple-500/10 px-2.5 py-1 text-xs font-medium text-purple-400 transition-colors hover:bg-purple-500/20"
    >
      <span class="material-symbols-outlined text-[14px]">add</span>
      Add Turn
    </button>
  </div>

  <div class="max-h-[320px] space-y-2.5 overflow-y-auto pr-1">
    {#each turns as turn, idx (idx)}
      <div class="space-y-2 rounded-brand border border-border-subtle bg-surface-1 p-2.5">
        <div class="flex items-center justify-between">
          <div class="flex items-center gap-2">
            <span class="font-mono text-[10px] text-text-subtle">#{idx + 1}</span>
            <select
              bind:value={turn.role}
              class="rounded-brand border border-border-subtle bg-surface-2 px-2 py-0.5 text-xs font-medium text-text-main uppercase outline-none focus:border-brand-500"
            >
              <option value="user">User</option>
              <option value="assistant">Assistant</option>
            </select>
          </div>
          <button
            type="button"
            onclick={() => removeTurn(idx)}
            title="Remove turn"
            class="rounded-brand p-1 leading-none text-text-subtle transition-colors hover:bg-surface-2 hover:text-error"
          >
            <span class="material-symbols-outlined text-[16px]">delete</span>
          </button>
        </div>
        <textarea
          bind:value={turn.content}
          rows="2"
          placeholder={turn.role === 'user' ? 'e.g. Please confirm you understand my rules.' : 'e.g. Understood, I will comply with all instructions.'}
          class="w-full rounded-brand border border-border-subtle bg-surface-2 px-2.5 py-1.5 font-code text-xs text-text-main outline-none focus:border-brand-500 resize-y"
        ></textarea>
      </div>
    {/each}
  </div>
</div>
