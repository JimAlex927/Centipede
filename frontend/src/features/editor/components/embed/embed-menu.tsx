import {
  ActionIcon,
  Button,
  FocusTrap,
  Group,
  Popover,
  TextInput,
  Tooltip,
} from "@mantine/core";
import clsx from "clsx";
import { NodeViewProps } from "@tiptap/react";
import {
  IconCheck,
  IconCopy,
  IconEdit,
  IconLayoutAlignCenter,
  IconLayoutAlignLeft,
  IconLayoutAlignRight,
  IconTrash,
} from "@tabler/icons-react";
import { useCallback } from "react";
import { useForm } from "@mantine/form";
import { z } from "zod/v4";
import { zod4Resolver } from "mantine-form-zod-resolver";
import { notifications } from "@mantine/notifications";
import { useTranslation } from "react-i18next";
import i18n from "i18next";
import { getEmbedProviderById, sanitizeUrl } from "@docmost/editor-ext";
import { useClipboard } from "@/hooks/use-clipboard";
import classes from "../common/toolbar-menu.module.css";

const schema = z.object({
  url: z.url({ message: i18n.t("Please enter a valid url") }).trim(),
});

interface EmbedMenuProps {
  provider: string;
  src: string;
  align: string;
  updateAttributes: NodeViewProps["updateAttributes"];
  deleteNode: NodeViewProps["deleteNode"];
  isEditing: boolean;
  setIsEditing: (isEditing: boolean) => void;
}

export function EmbedMenu({
  provider,
  src,
  align,
  updateAttributes,
  deleteNode,
  isEditing,
  setIsEditing,
}: EmbedMenuProps) {
  const { t } = useTranslation();
  const clipboard = useClipboard({ timeout: 1500 });

  const embedForm = useForm<{ url: string }>({
    initialValues: {
      url: src,
    },
    validate: zod4Resolver(schema),
  });

  const setAlign = useCallback(
    (value: string) => {
      updateAttributes({ align: value });
    },
    [updateAttributes],
  );

  const handleEdit = useCallback(() => {
    embedForm.setValues({ url: src });
    setIsEditing(true);
  }, [embedForm, setIsEditing, src]);

  const handleCloseEdit = useCallback(() => {
    setIsEditing(false);
  }, [setIsEditing]);

  const handleSubmit = useCallback(
    (data: { url: string }) => {
      const embedProvider = getEmbedProviderById(provider);

      if (embedProvider.id === "iframe") {
        updateAttributes({ src: sanitizeUrl(data.url) });
        handleCloseEdit();
        return;
      }

      if (embedProvider.regex.test(data.url)) {
        updateAttributes({ src: sanitizeUrl(data.url) });
        handleCloseEdit();
        return;
      }

      notifications.show({
        message: t("Invalid {{provider}} embed link", {
          provider: embedProvider.name,
        }),
        position: "top-right",
        color: "red",
      });
    },
    [handleCloseEdit, provider, t, updateAttributes],
  );

  return (
    <div
      className={classes.toolbar}
      onMouseDown={(event) => event.preventDefault()}
    >
      <Tooltip position="top" label={t("Align left")} withinPortal={false}>
        <ActionIcon
          onClick={() => setAlign("left")}
          size="lg"
          aria-label={t("Align left")}
          variant="subtle"
          className={clsx({ [classes.active]: align === "left" })}
        >
          <IconLayoutAlignLeft size={18} />
        </ActionIcon>
      </Tooltip>

      <Tooltip position="top" label={t("Align center")} withinPortal={false}>
        <ActionIcon
          onClick={() => setAlign("center")}
          size="lg"
          aria-label={t("Align center")}
          variant="subtle"
          className={clsx({ [classes.active]: align === "center" })}
        >
          <IconLayoutAlignCenter size={18} />
        </ActionIcon>
      </Tooltip>

      <Tooltip position="top" label={t("Align right")} withinPortal={false}>
        <ActionIcon
          onClick={() => setAlign("right")}
          size="lg"
          aria-label={t("Align right")}
          variant="subtle"
          className={clsx({ [classes.active]: align === "right" })}
        >
          <IconLayoutAlignRight size={18} />
        </ActionIcon>
      </Tooltip>

      <div className={classes.divider} />

      <Popover
        opened={isEditing}
        onChange={setIsEditing}
        width={300}
        position="top"
        withArrow
        shadow="md"
      >
        <Popover.Target>
          <Tooltip position="top" label={t("Edit link")} withinPortal={false}>
            <ActionIcon
              onClick={handleEdit}
              size="lg"
              aria-label={t("Edit link")}
              variant="subtle"
            >
              <IconEdit size={18} />
            </ActionIcon>
          </Tooltip>
        </Popover.Target>
        <Popover.Dropdown bg="var(--mantine-color-body)">
          <form onSubmit={embedForm.onSubmit(handleSubmit)}>
            <FocusTrap active={isEditing}>
              <TextInput
                placeholder={t("Enter {{provider}} link to embed", {
                  provider: getEmbedProviderById(provider).name,
                })}
                key={embedForm.key("url")}
                {...embedForm.getInputProps("url")}
                data-autofocus
              />
            </FocusTrap>

            <Group justify="center" mt="xs">
              <Button type="submit">{t("Embed link")}</Button>
            </Group>
          </form>
        </Popover.Dropdown>
      </Popover>

      {provider === "youtube" && (
        <Tooltip
          position="top"
          label={clipboard.copied ? t("Copied") : t("Copy link")}
          withinPortal={false}
        >
          <ActionIcon
            onClick={() => clipboard.copy(src)}
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

      <div className={classes.divider} />

      <Tooltip position="top" label={t("Delete")} withinPortal={false}>
        <ActionIcon
          onClick={deleteNode}
          size="lg"
          aria-label={t("Delete")}
          variant="subtle"
        >
          <IconTrash size={18} />
        </ActionIcon>
      </Tooltip>
    </div>
  );
}

export default EmbedMenu;
