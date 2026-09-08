import { BubbleMenu as BaseBubbleMenu } from "@tiptap/react/menus";
import { findParentNode, posToDOMRect, useEditorState } from "@tiptap/react";
import { useCallback } from "react";
import { Node as PMNode } from "@tiptap/pm/model";
import { isEditorReady } from "@docmost/editor-ext";
import {
  EditorMenuProps,
  ShouldShowProps,
} from "@/features/editor/components/table/types/types.ts";
import { ActionIcon, Tooltip } from "@mantine/core";
import clsx from "clsx";
import {
  IconCheck,
  IconCopy,
  IconLayoutAlignCenter,
  IconLayoutAlignLeft,
  IconLayoutAlignRight,
  IconTrash,
} from "@tabler/icons-react";
import { useTranslation } from "react-i18next";
import { useClipboard } from "@/hooks/use-clipboard";
import classes from "../common/toolbar-menu.module.css";

export function EmbedMenu({ editor }: EditorMenuProps) {
  const { t } = useTranslation();
  const clipboard = useClipboard({ timeout: 1500 });

  const editorState = useEditorState({
    editor,
    selector: (ctx) => {
      if (!ctx.editor) {
        return null;
      }

      const embedAttrs = ctx.editor.getAttributes("embed");

      return {
        isAlignLeft: ctx.editor.isActive("embed", { align: "left" }),
        isAlignCenter: ctx.editor.isActive("embed", { align: "center" }),
        isAlignRight: ctx.editor.isActive("embed", { align: "right" }),
        provider: embedAttrs?.provider || "",
        src: embedAttrs?.src || "",
      };
    },
  });

  const shouldShow = useCallback(
    ({ state }: ShouldShowProps) => {
      if (!state) {
        return false;
      }

      return editor.isActive("embed") && editor.getAttributes("embed").src;
    },
    [editor],
  );

  const getReferencedVirtualElement = useCallback(() => {
    if (!isEditorReady(editor)) return;
    const { selection } = editor.state;
    const predicate = (node: PMNode) => node.type.name === "embed";
    const parent = findParentNode(predicate)(selection);

    if (parent) {
      const dom = editor.view.nodeDOM(parent.pos) as HTMLElement;
      const domRect = dom.getBoundingClientRect();
      return {
        getBoundingClientRect: () => domRect,
        getClientRects: () => [domRect],
      };
    }

    const domRect = posToDOMRect(editor.view, selection.from, selection.to);
    return {
      getBoundingClientRect: () => domRect,
      getClientRects: () => [domRect],
    };
  }, [editor]);

  const alignLeft = useCallback(() => {
    editor
      .chain()
      .focus(undefined, { scrollIntoView: false })
      .updateAttributes("embed", { align: "left" })
      .run();
  }, [editor]);

  const alignCenter = useCallback(() => {
    editor
      .chain()
      .focus(undefined, { scrollIntoView: false })
      .updateAttributes("embed", { align: "center" })
      .run();
  }, [editor]);

  const alignRight = useCallback(() => {
    editor
      .chain()
      .focus(undefined, { scrollIntoView: false })
      .updateAttributes("embed", { align: "right" })
      .run();
  }, [editor]);

  const handleCopy = useCallback(() => {
    clipboard.copy(editorState?.src || "");
  }, [clipboard, editorState?.src]);

  const handleDelete = useCallback(() => {
    editor.commands.deleteSelection();
  }, [editor]);

  return (
    <BaseBubbleMenu
      editor={editor}
      pluginKey="embed-menu"
      updateDelay={0}
      getReferencedVirtualElement={getReferencedVirtualElement}
      options={{
        placement: "top",
        offset: 8,
        flip: false,
      }}
      shouldShow={shouldShow}
    >
      <div className={classes.toolbar}>
        <Tooltip position="top" label={t("Align left")} withinPortal={false}>
          <ActionIcon
            onClick={alignLeft}
            size="lg"
            aria-label={t("Align left")}
            variant="subtle"
            className={clsx({ [classes.active]: editorState?.isAlignLeft })}
          >
            <IconLayoutAlignLeft size={18} />
          </ActionIcon>
        </Tooltip>

        <Tooltip position="top" label={t("Align center")} withinPortal={false}>
          <ActionIcon
            onClick={alignCenter}
            size="lg"
            aria-label={t("Align center")}
            variant="subtle"
            className={clsx({ [classes.active]: editorState?.isAlignCenter })}
          >
            <IconLayoutAlignCenter size={18} />
          </ActionIcon>
        </Tooltip>

        <Tooltip position="top" label={t("Align right")} withinPortal={false}>
          <ActionIcon
            onClick={alignRight}
            size="lg"
            aria-label={t("Align right")}
            variant="subtle"
            className={clsx({ [classes.active]: editorState?.isAlignRight })}
          >
            <IconLayoutAlignRight size={18} />
          </ActionIcon>
        </Tooltip>

        <div className={classes.divider} />

        {editorState?.provider === "youtube" && (
          <Tooltip
            position="top"
            label={clipboard.copied ? t("Copied") : t("Copy link")}
            withinPortal={false}
          >
            <ActionIcon
              onClick={handleCopy}
              size="lg"
              aria-label={clipboard.copied ? t("Copied") : t("Copy link")}
              variant="subtle"
            >
              {clipboard.copied ? (
                <IconCheck size={18} />
              ) : (
                <IconCopy size={18} />
              )}
            </ActionIcon>
          </Tooltip>
        )}

        {editorState?.provider === "youtube" && (
          <div className={classes.divider} />
        )}

        <Tooltip position="top" label={t("Delete")} withinPortal={false}>
          <ActionIcon
            onClick={handleDelete}
            size="lg"
            aria-label={t("Delete")}
            variant="subtle"
          >
            <IconTrash size={18} />
          </ActionIcon>
        </Tooltip>
      </div>
    </BaseBubbleMenu>
  );
}

export default EmbedMenu;
