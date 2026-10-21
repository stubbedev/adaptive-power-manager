{
  perSystem =
    { lib, pkgs, ... }:
    let
      version = "0.2.0";
      apm = pkgs.buildGo127Module {
        pname = "adaptive-power-manager";
        inherit version;
        src = lib.fileset.toSource {
          root = ../.;
          fileset = lib.fileset.difference (lib.fileset.gitTracked ../.) (
            lib.fileset.unions [
              ../flake.nix
              ../flake.lock
              ../nix
            ]
          );
        };

        # vendorHash is kept current by .github/workflows/flake.yml on any
        # change to go.mod / go.sum. If you bump deps locally, run
        # `just sync-flake` and the correct hash lands here.
        vendorHash = "sha256-7WAGXf1vFnCPUxErA5lyF8YNDPyHkn2k1/3G/V6Ylnc=";

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
          license = lib.licenses.mit;
          mainProgram = "adaptive-power-manager";
          platforms = lib.platforms.linux;
        };
      };
    in
    {
      packages = {
        inherit apm;
        default = apm;
      };
    };
}
