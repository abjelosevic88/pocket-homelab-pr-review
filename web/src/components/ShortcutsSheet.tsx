import { Modal } from './ui'

const GROUPS: { title: string; keys: [string[], string][] }[] = [
  { title: 'Go to', keys: [[['h'], 'Waiting for you'], [['o'], 'All open'], [['m'], 'Yours'], [['s'], 'Reviews you sent'], [[','], 'Settings'], [['/'], 'Search & commands'], [['⌘', 'K'], 'Search & commands']] },
  { title: 'Lists', keys: [[['n'], 'Add a pull request'], [['r'], 'Check GitHub now']] },
  { title: 'In a pull request', keys: [[['j'], 'Next step'], [['k'], 'Previous step'], [['v'], 'Mark the step as looked at, then next'], [['1'], 'Walkthrough'], [['2'], 'As it was built'], [['3'], 'All files'], [['c'], 'Finish the review'], [['a'], 'Ask about this step'], [['e'], 'Hand the comments to a coding agent']] },
  { title: 'Window', keys: [[['['], 'Show or hide the sidebar'], [['esc'], 'Close a dialog or menu']] },
  { title: 'Help', keys: [[['?'], 'This sheet']] },
]

export function ShortcutsSheet({ onClose }: { onClose: () => void }) {
  return (
    <Modal title="Keyboard shortcuts" onClose={onClose} size="lg">
      <div className="shortcuts">
        {GROUPS.map(g => (
          <section key={g.title}>
            <h4 className="sc-h">{g.title}</h4>
            {g.keys.map(([keys, label]) => (
              <div key={label + keys.join('')} className="sc-row"><span>{label}</span><span className="sc-keys">{keys.map(k => <kbd key={k}>{k}</kbd>)}</span></div>
            ))}
          </section>
        ))}
      </div>
    </Modal>
  )
}
