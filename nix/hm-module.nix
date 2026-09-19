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
  tomlFormat = pkgs.formats.toml { };

  accountModule = lib.types.submodule (
    { config, ... }:
    {
      options = {
        jidFile = lib.mkOption {
          type = lib.types.path;
          description = "Path to a file containing the account's JID.";
        };
        passwordFile = lib.mkOption {
          type = lib.types.nullOr lib.types.path;
          default = null;
          description = "Path to a file containing the account's password.";
        };
        alias = lib.mkOption {
          type = lib.types.nullOr lib.types.str;
          default = null;
          description = "Display name shown in place of the JID in the UI.";
        };
        gpgKeyId = lib.mkOption {
          type = lib.types.nullOr lib.types.str;
          default = null;
          description = "Own GPG key ID, used to decrypt/sign.";
        };
        gpgPeers = lib.mkOption {
          type = lib.types.attrsOf lib.types.str;
          default = { };
          description = "Map of peer JID -> GPG key fingerprint.";
        };
        omemoPeers = lib.mkOption {
          type = lib.types.attrsOf lib.types.str;
          default = { };
          description = ''Map of peer JID -> pinned OMEMO protocol version ("v1" or "v2").'';
        };
        status = lib.mkOption {
          type = lib.types.nullOr lib.types.str;
          default = null;
          description = ''Configured presence: "chat", "away", "xa", "dnd", or "offline".'';
        };
      };
    }
  );

  accountsStaticJSON = builtins.toJSON (
    map (
      a:
      lib.filterAttrs (_: v: v != null && v != { }) {
        inherit (a) alias status;
        gpg_key_id = a.gpgKeyId;
        gpg_peers = if a.gpgPeers == { } then null else a.gpgPeers;
        omemo_peers = if a.omemoPeers == { } then null else a.omemoPeers;
      }
    ) cfg.accounts
  );

  patchSecret =
    i: field: file:
    lib.optionalString (file != null) ''
      value=$(cat ${lib.escapeShellArg file})
      accounts=$(${lib.getExe pkgs.jq} --arg v "$value" '.[${toString i}].${field} = $v' <<<"$accounts")
    '';

  accountsActivationScript = ''
    set -eu
    accounts=${lib.escapeShellArg accountsStaticJSON}
    ${lib.concatStrings (
      lib.imap0 (i: a: patchSecret i "jid" a.jidFile + patchSecret i "password" a.passwordFile) cfg.accounts
    )}
    full=$(${lib.getExe pkgs.jq} --argjson accts "$accounts" '. + {accounts: $accts}' <<<${
      lib.escapeShellArg (builtins.toJSON (builtins.removeAttrs cfg.settings [ "accounts" ]))
    })
    mkdir -p ${lib.escapeShellArg "${config.xdg.configHome}/kage"}
    printf '%s' "$full" | ${pkgs.remarshal}/bin/json2toml > ${
      lib.escapeShellArg "${config.xdg.configHome}/kage/config.toml"
    }
  '';
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

    settings = lib.mkOption {
      type = tomlFormat.type;
      default = { };
      description = ''
        Settings written verbatim to config.toml (keys as documented in
        kage's config.Config, e.g. `mouse_disabled`, `theme`, `keybinds`,
        `storage`, ...). Does not include runtime state (dragged sidebar
        width, last opened chat, cycled sort order, per-account presence,
        ...), which lives in a separate state.toml this module never
        touches - see config.State in kage's source for the full list.

        Must not set `accounts` when the `accounts` option below is used.
      '';
      example = lib.literalExpression ''
        {
          mouse_disabled = true;
          default_encryption_mode = "omemo-v2";
          theme.app_bg = "#000000";
          keybinds.quit = [ "q" "ctrl+c" ];
        }
      '';
    };

    accounts = lib.mkOption {
      type = lib.types.listOf accountModule;
      default = [ ];
      description = "XMPP accounts, merged into config.toml's `[[accounts]]` array.";
      example = lib.literalExpression ''
        [
          {
            jidFile = config.sops.secrets.kage-jid.path;
            passwordFile = config.sops.secrets.kage-password.path;
            alias = "work";
          }
        ]
      '';
    };

    systemd.enable = lib.mkEnableOption "a systemd user service that starts/stops the kage daemon with the graphical session";
  };

  config = lib.mkIf cfg.enable {
    home.packages = [ cfg.package ];

    assertions = [
      {
        assertion = cfg.accounts == [ ] || !(cfg.settings ? accounts);
        message = "programs.kage: set accounts via either `settings.accounts` or `accounts`, not both";
      }
    ];

    xdg.configFile."kage/config.toml" = lib.mkIf (cfg.settings != { } && cfg.accounts == [ ]) {
      source = tomlFormat.generate "kage-config.toml" cfg.settings;
    };

    home.activation.kageAccounts = lib.mkIf (cfg.accounts != [ ]) (
      lib.hm.dag.entryAfter [ "writeBoundary" ] ''
        run bash -c ${lib.escapeShellArg accountsActivationScript}
      ''
    );

    systemd.user.services.kage = lib.mkIf cfg.systemd.enable {
      Unit = {
        Description = "kage background service";
        PartOf = [ "graphical-session.target" ];
        After = [ "graphical-session.target" ];
      };
      Service = {
        Type = "oneshot";
        RemainAfterExit = true;
        ExecStart = "${cfg.package}/bin/kage daemon start";
        ExecStop = "${cfg.package}/bin/kage daemon stop";
      };
      Install.WantedBy = [ "graphical-session.target" ];
    };
  };
}
