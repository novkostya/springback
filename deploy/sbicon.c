// sbicon — ask a device for the icons it draws on its own home screen.
//
// WHY THIS EXISTS AS A BINARY WE BUILD OURSELVES. Every other device call springback makes goes
// through a tool libimobiledevice ships: idevice_id, ideviceinfo, ideviceinstaller. There is no
// shipped tool for SpringBoard services, so this is the one gap the project fills with its own
// forty lines of C rather than an argv.
//
// WHY NOT THE ITUNES API. The lookup endpoint returns artworkUrl512, and it returns it only for
// apps that are still listed — which is precisely backwards for a screen whose subject is apps
// that are NOT. For an app that has been delisted and never archived there is no store record and
// no local .ipa; the device itself is the only thing left that still has the picture. Measured
// against a real device: 214 of 214 installed apps returned an icon, delisted ones included.
//
// The device does the rendering, so what comes back is a plain PNG — already masked to the home
// screen's rounded shape, no CgBI mangling, nothing to decode. That is worth knowing because the
// library's icons, read out of the .ipa, need a whole file of work to become viewable.
//
// BATCHED ON PURPOSE. Connecting costs ~450ms and each icon after that costs ~11ms, so the shape
// of this program — one connection, many icons — is the difference between 2.7 seconds for a
// whole device and forty times that.
#include <errno.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <libimobiledevice/libimobiledevice.h>
#include <libimobiledevice/sbservices.h>

int main(int argc, char **argv) {
	if (argc < 4) {
		fprintf(stderr, "usage: sbicon <udid> <outdir> <bundleid>...\n");
		return 64;
	}
	const char *udid = argv[1];
	const char *outdir = argv[2];

	idevice_t dev = NULL;
	idevice_error_t derr = idevice_new_with_options(&dev, udid,
		(enum idevice_options)(IDEVICE_LOOKUP_NETWORK | IDEVICE_LOOKUP_USBMUX));
	if (derr != IDEVICE_E_SUCCESS) {
		// A sleeping iPhone is not an error worth shouting about — it drops off the network
		// entirely and springback already renders that as "not currently reachable".
		fprintf(stderr, "sbicon: no device %s (%d)\n", udid, derr);
		return 1;
	}

	sbservices_client_t sb = NULL;
	sbservices_error_t serr = sbservices_client_start_service(dev, &sb, "springback");
	if (serr != SBSERVICES_E_SUCCESS) {
		fprintf(stderr, "sbicon: start_service: %d\n", serr);
		idevice_free(dev);
		return 2;
	}

	int ok = 0;
	for (int i = 3; i < argc; i++) {
		char *png = NULL;
		uint64_t len = 0;
		serr = sbservices_get_icon_pngdata(sb, argv[i], &png, &len);
		if (serr != SBSERVICES_E_SUCCESS || !png || len == 0) {
			// PER-APP FAILURES ARE NOT FATAL. An app can be uninstalled between the moment
			// its bundle id was listed and the moment its icon is asked for, and one such
			// app must not cost the other two hundred their icons.
			fprintf(stderr, "sbicon: %s: %d\n", argv[i], serr);
			free(png);
			continue;
		}
		// The caller passes bundle ids it has already checked, and reads back by the same
		// name. Nothing here builds a path out of anything the device said.
		char path[2048];
		if (snprintf(path, sizeof(path), "%s/%s.png", outdir, argv[i]) >= (int)sizeof(path)) {
			fprintf(stderr, "sbicon: %s: path too long\n", argv[i]);
			free(png);
			continue;
		}
		FILE *f = fopen(path, "wb");
		if (!f) {
			fprintf(stderr, "sbicon: %s: %s\n", argv[i], strerror(errno));
			free(png);
			continue;
		}
		size_t n = fwrite(png, 1, (size_t)len, f);
		int cerr = fclose(f);
		free(png);
		if (n != (size_t)len || cerr != 0) {
			// A short write leaves a truncated PNG on disk, which would be cached and
			// served as a broken image forever. Remove it and let it be a miss.
			remove(path);
			fprintf(stderr, "sbicon: %s: short write\n", argv[i]);
			continue;
		}
		ok++;
	}

	sbservices_client_free(sb);
	idevice_free(dev);
	fprintf(stderr, "sbicon: %d/%d\n", ok, argc - 3);
	// Exit 0 whenever the CONNECTION worked, even if no icon did: the caller distinguishes
	// "the device would not talk to us" from "this app has no icon" by which files appeared.
	return 0;
}
