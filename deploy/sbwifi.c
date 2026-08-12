// sbwifi — read and write the device's Wi-Fi sync flag.
//
// WHY A HELPER AND NOT A FLAG ON ideviceinfo. The shipped ideviceinfo can only READ lockdown
// values; there is no upstream way to write one from the command line. The alternative is
// building libimobiledevice from source with a patch that adds `--set-bool` — which means owning
// a patch queue against someone else's C project in order to flip one boolean. Forty lines
// against the same library the image already links is the smaller debt.
//
// WHAT THE FLAG DOES. com.apple.mobile.wireless_lockdown/EnableWifiConnections is what makes a
// device reachable when it is not plugged in. With it off, the device only answers over USB and
// drops out of `idevice_id -n` entirely.
//
// TURNING IT OFF OVER WI-FI CUTS THE BRANCH YOU ARE SITTING ON. The write is accepted, the device
// leaves the network, and the read-back then fails because there is no longer a connection to
// read over. That is success, not failure, and only the caller knows which action it asked for —
// so this program reports what happened and leaves the judgement to Go.
#include <stdio.h>
#include <string.h>
#include <libimobiledevice/libimobiledevice.h>
#include <libimobiledevice/lockdown.h>
#include <plist/plist.h>

static const char *DOMAIN = "com.apple.mobile.wireless_lockdown";
static const char *KEY = "EnableWifiConnections";

// connect opens a lockdown session. Returns 0 on success.
static int connect_lockdown(const char *udid, idevice_t *dev, lockdownd_client_t *client) {
	idevice_error_t derr = idevice_new_with_options(dev, udid,
		(enum idevice_options)(IDEVICE_LOOKUP_NETWORK | IDEVICE_LOOKUP_USBMUX));
	if (derr != IDEVICE_E_SUCCESS) {
		fprintf(stderr, "sbwifi: no device %s (%d)\n", udid, derr);
		return 1;
	}
	// WITH a handshake: reading and writing this domain needs a trusted session, which is the
	// same pairing record everything else here depends on.
	lockdownd_error_t lerr = lockdownd_client_new_with_handshake(*dev, client, "springback");
	if (lerr != LOCKDOWN_E_SUCCESS) {
		fprintf(stderr, "sbwifi: lockdown handshake failed (%d)\n", lerr);
		idevice_free(*dev);
		*dev = NULL;
		return 2;
	}
	return 0;
}

// read_flag prints "on", "off", or nothing at all when the key cannot be read.
static int read_flag(lockdownd_client_t client, const char **out) {
	plist_t value = NULL;
	if (lockdownd_get_value(client, DOMAIN, KEY, &value) != LOCKDOWN_E_SUCCESS || !value) {
		return 1;
	}
	uint8_t b = 0;
	if (plist_get_node_type(value) != PLIST_BOOLEAN) {
		plist_free(value);
		return 1;
	}
	plist_get_bool_val(value, &b);
	plist_free(value);
	*out = b ? "on" : "off";
	return 0;
}

int main(int argc, char **argv) {
	if (argc < 3) {
		fprintf(stderr, "usage: sbwifi get <udid> | sbwifi set <udid> <on|off>\n");
		return 64;
	}
	const char *cmd = argv[1];
	const char *udid = argv[2];

	idevice_t dev = NULL;
	lockdownd_client_t client = NULL;
	int rc = connect_lockdown(udid, &dev, &client);
	if (rc != 0) {
		return rc;
	}

	int status = 0;
	if (strcmp(cmd, "get") == 0) {
		const char *state = NULL;
		if (read_flag(client, &state) != 0) {
			// UNKNOWN, NOT OFF. An absent or unreadable key says nothing about the
			// setting, and printing "off" here would be a confident lie about a device
			// that simply did not answer.
			printf("unknown\n");
		} else {
			printf("%s\n", state);
		}
	} else if (strcmp(cmd, "set") == 0 && argc >= 4) {
		int want_on = strcmp(argv[3], "on") == 0;
		plist_t value = plist_new_bool(want_on);
		lockdownd_error_t lerr = lockdownd_set_value(client, DOMAIN, KEY, value);
		if (lerr != LOCKDOWN_E_SUCCESS) {
			fprintf(stderr, "sbwifi: set failed (%d)\n", lerr);
			status = 3;
		} else {
			// READ BACK, because a device accepting a request is not the same as a
			// device applying it. Whether a failed read-back is a problem depends on
			// what was asked for — switching Wi-Fi sync off over Wi-Fi removes the
			// only route the read-back could take — so both outcomes are reported
			// plainly and Go decides.
			const char *state = NULL;
			if (read_flag(client, &state) != 0) {
				printf("unreadable\n");
			} else {
				printf("%s\n", state);
			}
		}
	} else {
		fprintf(stderr, "sbwifi: unknown command %s\n", cmd);
		status = 64;
	}

	lockdownd_client_free(client);
	idevice_free(dev);
	return status;
}
