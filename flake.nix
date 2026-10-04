{
  description = "Adaptive battery charging and power profile policy for Linux laptops";

  # Offer the prebuilt CI cache so consumers fetch the binary instead of
  # rebuilding it. Honoured with --accept-flake-config (or for trusted users).
  # The cache is public; no token needed to pull.
  nixConfig = {
    extra-substituters = [ "https://nix.stubbe.dev/default" ];
    extra-trusted-public-keys = [ "default:9P4FePqHV1rGv5NDBun0GN26y83pcaaMr/NHZrxKaac=" ];
  };

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs =
    { self, nixpkgs }:
    let
      # The daemon reads Linux sysfs and talks to system D-Bus services; a
      # darwin build would compile and be useless.
      systems = [
        "x86_64-linux"
        "aarch64-linux"
      ];
      forAllSystems = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
      version = "0.1.0";
    in
    {
      packages = forAllSystems (
        pkgs:
        let
          apm = pkgs.buildGo127Module {
            pname = "adaptive-power-manager";
            inherit version;
            src = self;

            # vendorHash is kept current by .github/workflows/flake.yml on any
            # change to go.mod / go.sum. If you bump deps locally, run
            # `just sync-flake` and the correct hash lands here.
            vendorHash = "sha256-WUTGAYigUjuZLHO1YpVhFSWpvULDZfGMfOXZQqVYAfs=";

            # Static pure-Go binary: no cgo, no dynamic resolver, runs on
            # any Linux with the same thresholds contract.
            env.CGO_ENABLED = "0";
            tags = [
              "osusergo"
              "netgo"
            ];
            ldflags = [
              "-s"
              "-w"
              "-X"
              "main.version=${version}"
            ];
            doCheck = true;

            meta = {
              description = "Adaptive battery charging and power profile policy for Linux laptops";
              homepage = "https://github.com/stubbedev/adaptive-power-manager";
              license = pkgs.lib.licenses.mit;
              mainProgram = "adaptive-power-manager";
              platforms = pkgs.lib.platforms.linux;
            };
          };
        in
        {
          inherit apm;
          default = apm;
        }
      );

      apps = forAllSystems (pkgs: rec {
        adaptive-power-manager = {
          type = "app";
          program = "${self.packages.${pkgs.system}.adaptive-power-manager}/bin/adaptive-power-manager";
        };
        default = adaptive-power-manager;
      });

      devShells = forAllSystems (pkgs: {
        default = pkgs.mkShell {
          packages = [
            pkgs.go_1_27
            pkgs.gopls
            pkgs.gotools
            pkgs.golangci-lint
            pkgs.just
          ];
        };
      });

      formatter = forAllSystems (pkgs: pkgs.nixpkgs-fmt);
    };
}
