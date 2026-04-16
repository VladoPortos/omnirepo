/**
 * Milkdown WYSIWYG markdown editor wrapper per D-18.
 * ProseMirror-based, renders through React. Used for repo README editing.
 */

import { Editor, rootCtx, defaultValueCtx } from '@milkdown/kit/core';
import { commonmark } from '@milkdown/kit/preset/commonmark';
import { Milkdown, MilkdownProvider, useEditor } from '@milkdown/react';
import { nord } from '@milkdown/theme-nord';
import { listener, listenerCtx } from '@milkdown/kit/plugin/listener';

interface Props {
  value: string;
  onChange: (markdown: string) => void;
}

function MilkdownEditor({ value, onChange }: Props) {
  useEditor((root) =>
    Editor.make()
      .config(nord)
      .config((ctx) => {
        ctx.set(rootCtx, root);
        ctx.set(defaultValueCtx, value);
        ctx.get(listenerCtx).markdownUpdated((_ctx, md) => onChange(md));
      })
      .use(commonmark)
      .use(listener),
  );
  return <Milkdown />;
}

export function MarkdownEditor(props: Props) {
  return (
    <MilkdownProvider>
      <MilkdownEditor {...props} />
    </MilkdownProvider>
  );
}
