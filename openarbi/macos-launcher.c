// Native entrypoint: code signatures survive archive extraction without xattrs.
#include <mach-o/dyld.h>
#include <limits.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

int main(int argc, char **argv) {
    (void)argc;
    char executable[PATH_MAX], resolved[PATH_MAX], launcher[PATH_MAX];
    uint32_t size = sizeof(executable);
    if (_NSGetExecutablePath(executable, &size) != 0 || !realpath(executable, resolved)) {
        fputs("OpenArbi: cannot locate the installed application\n", stderr);
        return 1;
    }
    // .../OpenArbi.app/Contents/MacOS/openarbi -> release root.
    for (int i = 0; i < 4; i++) {
        char *slash = strrchr(resolved, '/');
        if (!slash) {
            fputs("OpenArbi: invalid application layout\n", stderr);
            return 1;
        }
        *slash = '\0';
    }
    if (snprintf(launcher, sizeof(launcher), "%s/bin/openarbi", resolved) >= (int)sizeof(launcher)) {
        fputs("OpenArbi: installation path is too long\n", stderr);
        return 1;
    }
    argv[0] = launcher;
    execv(launcher, argv);
    perror("OpenArbi: cannot start installed launcher");
    return 1;
}
