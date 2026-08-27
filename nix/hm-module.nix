{
  self,
}:
{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.programs.kage;
  yamlFormat = pkgs.formats.yaml { };

  themeOption =
    lib.mkOption {
      type = lib.types.submodule {
        options = lib.genAttrs [
          "app_bg"
          "panel_bg"
          "panel_alt_bg"
          "panel_edge"
          "log_bg"
          "them_fg"
          "text_muted"
          "time"
          "border_d"
          "border_a"
          "accent_cyan"
          "reply_fg"
          "popup_bg"
          "popup_danger"
          "filter_match"
          "nick_me"
          "nick_them"
          "status_fg"
          "notice_bg"
          "notice_fg"
        ] (_: lib.mkOption {
          type = lib.types.nullOr lib.types.str;
          default = null;
          description = "Overrides the built-in Tokyo Night default for this color; unset inherits it.";
        });
      };
      default = { };
      description = ''
        Per-color overrides on top of kage's built-in theme. Any field left
        null keeps the built-in default.
      '';
    };

  accountOpts =
    { name, ... }:
    {
      options = {
        jid = lib.mkOption {
          type = lib.types.str;
          default = name;
          description = "The account's JID.";
        };
        alias = lib.mkOption {
          type = lib.types.nullOr lib.types.str;
          default = null;
          description = "Display name shown in place of the JID in the UI.";
        };
        passwordCmd = lib.mkOption {
          type = lib.types.nullOr lib.types.str;
          default = null;
          description = ''
            Shell command printing the account password on stdout. Preferred
            over `password` (below) so the secret doesn't land in the Nix
            store in plaintext - e.g. a command reading from `pass`,
            `sops-nix`, or the OS keyring.
          '';
        };
        password = lib.mkOption {
          type = lib.types.nullOr lib.types.str;
          default = null;
          description = ''
            Plaintext password fallback. WARNING: this value is copied
            verbatim into the Nix store (world-readable on multi-user
            systems) and into the generated config.yaml. Prefer
            `passwordCmd` for anything but throwaway/test accounts.
          '';
        };
        gpgKeyId = lib.mkOption {
          type = lib.types.nullOr lib.types.str;
          default = null;
          description = "Own GPG key ID, used to decrypt/sign for this account.";
        };
        gpgPeers = lib.mkOption {
          type = lib.types.attrsOf lib.types.str;
          default = { };
          description = "Peer JID -> GPG key fingerprint, for chats pinned to gpg mode.";
        };
        omemoPeers = lib.mkOption {
          type = lib.types.attrsOf (lib.types.enum [
            "v1"
            "v2"
          ]);
          default = { };
          description = "Peer JID -> pinned OMEMO protocol version.";
        };
      };
    };
in
{
  options.programs.kage = {
    enable = lib.mkEnableOption "kage, a TUI XMPP client";

    package = lib.mkOption {
      type = lib.types.package;
      default = self.packages.${pkgs.stdenv.hostPlatform.system}.default;
      defaultText = lib.literalExpression "kage.packages.<system>.default";
      description = "The kage package to install.";
    };

    # --- behavior toggles -------------------------------------------------
    mouseDisabled = lib.mkOption {
      type = lib.types.bool;
      default = false;
      description = "Disable mouse click/scroll support.";
    };
    iconsDisabled = lib.mkOption {
      type = lib.types.bool;
      default = false;
      description = "Hide attachment/encryption icons in favor of plain-text tags.";
    };
    filePickerFilesFirst = lib.mkOption {
      type = lib.types.bool;
      default = false;
      description = "Show files before directories in the attach-file picker.";
    };
    showNames = lib.mkOption {
      type = lib.types.bool;
      default = false;
      description = "Show the sender's name in the message header instead of just a direction glyph.";
    };
    timeLayout = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      description = ''A custom Go time layout for message timestamps (default: "15:04"/"2006-01-02 15:04").'';
    };
    alwaysShowFullDate = lib.mkOption {
      type = lib.types.bool;
      default = false;
      description = "Always show the full date, even for messages sent today.";
    };
    openLastChatDisabled = lib.mkOption {
      type = lib.types.bool;
      default = false;
      description = "Don't reopen the last opened chat on startup.";
    };
    notificationsDisabled = lib.mkOption {
      type = lib.types.bool;
      default = false;
      description = "Disable desktop notifications for decrypted incoming messages.";
    };
    terminalCmd = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      description = "Terminal emulator launched from the tray icon (default: $TERMINAL, then xdg-terminal-exec, then a hardcoded list).";
    };
    attachmentsDir = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      description = "Directory decrypted/downloaded attachments are cached in (default: $XDG_CACHE_HOME/kage/attachments).";
    };
    gpgDisabled = lib.mkOption {
      type = lib.types.bool;
      default = false;
      description = "Disable GPG encryption entirely.";
    };
    keyringDisabled = lib.mkOption {
      type = lib.types.bool;
      default = false;
      description = "Never consult the OS keyring.";
    };
    showEncryptedIcon = lib.mkOption {
      type = lib.types.bool;
      default = false;
      description = "Show a lock icon/tag next to encrypted messages.";
    };
    defaultEncryptionMode = lib.mkOption {
      type = lib.types.nullOr (
        lib.types.enum [
          "omemo-v1"
          "omemo-v2"
          "gpg"
          "none"
        ]
      );
      default = null;
      description = "Outgoing encryption mode a chat starts with before the user picks one (default: omemo-v1).";
    };
    debug = lib.mkOption {
      type = lib.types.bool;
      default = false;
      description = "Log at debug level to <config dir>/kage/debug.log instead of warn.";
    };
    historyPageSize = lib.mkOption {
      type = lib.types.nullOr lib.types.ints.positive;
      default = null;
      description = "Messages loaded per chat at a time (default: 60).";
    };
    maxMessagesPerChat = lib.mkOption {
      type = lib.types.nullOr lib.types.ints.positive;
      default = null;
      description = "Cap on messages kept in memory/view per chat (default: 120).";
    };
    noticeDuration = lib.mkOption {
      type = lib.types.nullOr lib.types.ints.positive;
      default = null;
      description = "Seconds an in-app notification toast stays visible (default: 3).";
    };
    videoQuality = lib.mkOption {
      type = lib.types.nullOr (
        lib.types.enum [
          "very_low"
          "low"
          "medium"
          "high"
        ]
      );
      default = null;
      description = "Capture profile for outgoing screen-share/camera video (default: medium).";
    };

    # --- storage ------------------------------------------------------------
    storage = {
      password = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = ''
          Plaintext fallback password local message history is encrypted
          under at rest. WARNING: copied verbatim into the Nix store. Prefer
          `storage.passwordCmd`.
        '';
      };
      passwordCmd = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = "Shell command printing the local-storage password on stdout.";
      };
    };

    # --- theme/keybinds -------------------------------------------------
    theme = themeOption;
    keybinds = lib.mkOption {
      type = yamlFormat.type;
      default = { };
      example = {
        quit = [
          "q"
          "ctrl+c"
        ];
      };
      description = "Keybind overrides: action name -> key string or list of key strings.";
    };

    # --- accounts ---------------------------------------------------------
    accounts = lib.mkOption {
      type = lib.types.attrsOf (lib.types.submodule accountOpts);
      default = { };
      description = "Configured XMPP accounts, keyed by an arbitrary name (jid defaults to the key).";
      example = lib.literalExpression ''
        {
          "me@example.org" = {
            passwordCmd = "cat ''${config.sops.secrets.kage-xmpp-password.path}";
          };
        }
      '';
    };
    defaultAccount = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      description = ''
        JID selected on startup (default: the first configured account).
        Note: this seeds only the very first run - once the app is used, the
        user's own account switches are persisted to state.yaml (outside
        Nix's control) and take precedence.
      '';
    };

    extraConfig = lib.mkOption {
      type = yamlFormat.type;
      default = { };
      description = "Extra config.yaml keys not covered by a dedicated option, merged in verbatim (escape hatch).";
    };
  };

  config = lib.mkIf cfg.enable {
    home.packages = [ cfg.package ];

    xdg.configFile."kage/config.yaml".source =
      let
        boolOr = v: if v then v else null;
        accountToYaml = a: {
          jid = a.jid;
          alias = a.alias;
          password = a.password;
          password_cmd = a.passwordCmd;
          gpg_key_id = a.gpgKeyId;
          gpg_peers = if a.gpgPeers == { } then null else a.gpgPeers;
          omemo_peers = if a.omemoPeers == { } then null else a.omemoPeers;
        };
        settings = {
          mouse_disabled = boolOr cfg.mouseDisabled;
          icons_disabled = boolOr cfg.iconsDisabled;
          file_picker_files_first = boolOr cfg.filePickerFilesFirst;
          show_names = boolOr cfg.showNames;
          time_layout = cfg.timeLayout;
          always_show_full_date = boolOr cfg.alwaysShowFullDate;
          open_last_chat_disabled = boolOr cfg.openLastChatDisabled;
          notifications_disabled = boolOr cfg.notificationsDisabled;
          terminal_cmd = cfg.terminalCmd;
          attachments_dir = cfg.attachmentsDir;
          gpg_disabled = boolOr cfg.gpgDisabled;
          keyring_disabled = boolOr cfg.keyringDisabled;
          show_encrypted_icon = boolOr cfg.showEncryptedIcon;
          default_encryption_mode = cfg.defaultEncryptionMode;
          debug = boolOr cfg.debug;
          history_page_size = cfg.historyPageSize;
          max_messages_per_chat = cfg.maxMessagesPerChat;
          notice_duration = cfg.noticeDuration;
          video_quality = cfg.videoQuality;
          default_account = cfg.defaultAccount;
          storage =
            if cfg.storage.password == null && cfg.storage.passwordCmd == null then
              null
            else
              {
                password = cfg.storage.password;
                password_cmd = cfg.storage.passwordCmd;
              };
          theme = if cfg.theme == { } then null else cfg.theme;
          keybinds = if cfg.keybinds == { } then null else cfg.keybinds;
          accounts =
            if cfg.accounts == { } then null else map accountToYaml (lib.attrValues cfg.accounts);
        };
        merged = lib.recursiveUpdate (lib.filterAttrsRecursive (_: v: v != null) settings) cfg.extraConfig;
      in
      yamlFormat.generate "kage-config.yaml" merged;
  };
}
