/* The install commands, once. Strings only: no `$` prompt, so a page can show
 * them as they are and a copy button copies what it shows. install-commands.test.ts
 * checks each against the docs it comes from. */

/** docs/QUICKSTART.md, "Run it". */
export const DOCKER_RUN = `docker run -d --name polyemesis \\
  -p 8080:8080 -p 6000:6000/udp -p 1935:1935 \\
  -v polyemesis-data:/data \\
  rainmanjam/polyemesis:latest`;

/** docs/INSTALL.md, Compose: show CLONE, then COMPOSE_UP. */
export const CLONE = `git clone https://github.com/rainmanjam/polyemesis
cd polyemesis`;

export const COMPOSE_UP = "docker compose up -d";

/** docs/INSTALL.md, "The scripted install". */
export const INSTALL_SCRIPT = `curl --proto '=https' --proto-redir '=https' --tlsv1.2 -fsSLO \\
  https://raw.githubusercontent.com/rainmanjam/polyemesis/main/scripts/install.sh
less install.sh          # it runs as root; read it first
sudo bash install.sh`;
