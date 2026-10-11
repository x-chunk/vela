"""Keep source and examples independent of any private deployment."""
import ipaddress
import pathlib
import re
import subprocess
import sys

# Project identity: preserve the public Go module and documentation URL.
DOMAIN = "xchunk" + ".org"
ALLOWED_DOMAINS = re.compile(r"(?<![A-Za-z0-9.-])(?:go|docs)\." + re.escape(DOMAIN) + r"/vela(?=$|[^A-Za-z0-9_.-])", re.I)
# Reserved documentation addresses, loopback, and wildcard listeners are neutral.
DOCUMENTATION_NETS = tuple(ipaddress.ip_network(n) for n in (
    "192.0.2.0/24", "198.51.100.0/24", "203.0.113.0/24", "2001:db8::/32",
))
# This single gateway is an explicitly labeled Docker example, never a default.
DOCKER_EXAMPLE = ipaddress.ip_address("172.17.0.1")
FORBIDDEN = re.compile("|".join((
    re.escape("ghcr" + ".io"), "GO" + "PRIVATE",
    r"/ho" + r"me/[A-Za-z0-9_-]+", r"/Us" + r"ers/[A-Za-z0-9_-]+",
    r"\bedu-publish\b", r"\bedu/", r"\bhyprland\b",
)), re.I)
IPV4 = re.compile(r"(?<![A-Za-z0-9_.])(?:\d{1,3}\.){3}\d{1,3}(?![A-Za-z0-9_.])")
IPV6 = re.compile(r"(?<![A-Za-z0-9.])(?:[0-9a-fA-F]{0,4}:){2,}[0-9a-fA-F.]*(?![A-Za-z0-9])")


def problems(line):
    """Return categories without echoing potentially sensitive source contents."""
    neutral = ALLOWED_DOMAINS.sub("", line)
    if DOMAIN in neutral.lower():
        yield "deployment domain"
    if FORBIDDEN.search(line):
        yield "deployment-specific text"
    for pattern in (IPV4, IPV6):
        for match in pattern.finditer(line):
            try:
                addr = ipaddress.ip_address(match.group())
            except ValueError:
                continue
            if addr.is_loopback or addr.is_unspecified or addr == DOCKER_EXAMPLE:
                continue
            if any(addr.version == net.version and addr in net for net in DOCUMENTATION_NETS):
                continue
            yield "address outside documentation ranges"


def main():
    """Check Git's source inventory, including new files before they are committed."""
    root = pathlib.Path(sys.argv[1]).resolve()
    output = subprocess.check_output([
        "git", "-C", str(root), "ls-files", "-z", "--cached", "--others", "--exclude-standard",
    ])
    failed = False
    for name in sorted(set(output.decode().split("\0")) - {""}):
        path = root / name
        if not path.is_file():
            continue  # Deleted files are absent from the resulting source tree.
        data = path.read_bytes()
        if b"\0" in data:
            continue
        try:
            lines = data.decode("utf-8").splitlines()
        except UnicodeDecodeError:
            continue
        for number, line in enumerate(lines, 1):
            for problem in problems(line):
                print(f"{name}:{number}: {problem}", file=sys.stderr)
                failed = True
    return int(failed)


if __name__ == "__main__":
    sys.exit(main())
