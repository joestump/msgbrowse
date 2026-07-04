//go:build desktop

// Objective-C definitions for tray_platform_darwin.go (issue #167) — see that
// file's comment for the full rationale. Kept out of the cgo preamble because
// the Go file uses //export, whose preamble may carry declarations only.
//
// All three entry points dispatch onto the GCD main queue: AppKit calls
// ([NSApp setActivationPolicy:], NSStatusItem creation via the systray start
// callback, notification-center registration used from blocks) belong on the
// main thread, and main-queue blocks enqueued before [NSApp run] begins are
// executed on its first turn — i.e. strictly after the application finished
// launching, which is the safe moment to install a status item.

#import <Cocoa/Cocoa.h>

// Exported from tray_platform_darwin.go (cgo _cgo_export.h prototype).
extern void msgbrowseTrayStartCallback(void);

void msgbrowse_schedule_tray_start(void) {
	dispatch_async(dispatch_get_main_queue(), ^{
		msgbrowseTrayStartCallback();
	});
}

void msgbrowse_set_activation_policy(int accessory) {
	dispatch_async(dispatch_get_main_queue(), ^{
		if (accessory) {
			// Menubar-only residency: no Dock icon, no Cmd+Tab entry.
			[NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];
		} else {
			[NSApp setActivationPolicy:NSApplicationActivationPolicyRegular];
			// Accessory→regular leaves the app inactive; activate so the
			// restored window actually orders front and takes focus.
			[NSApp activateIgnoringOtherApps:YES];
		}
	});
}

void msgbrowse_enable_dock_autohide(void) {
	dispatch_async(dispatch_get_main_queue(), ^{
		// Close-to-tray hides the whole app (Wails HideWindowOnClose runs
		// [NSApp hide:]); hiding is the moment to leave the Dock. Armed only
		// after the tray watchdog confirmed the status item registered.
		[[NSNotificationCenter defaultCenter]
		    addObserverForName:NSApplicationDidHideNotification
		                object:nil
		                 queue:[NSOperationQueue mainQueue]
		            usingBlock:^(NSNotification *note) {
			[NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];
		}];
	});
}
