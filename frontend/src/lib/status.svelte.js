import { Status } from '$lib/wailsjs/go/cmd/App.js';

/** @typedef {{ready: boolean, problem: string, dataDir: string, folders: string[], watching: boolean, assets: number, versions: number, openTickets: number, toReview: number, unreadable: string[], appVersion: string}} AppStatus */

/** @type {AppStatus} */
const empty = {
	ready: false,
	problem: '',
	dataDir: '',
	folders: [],
	watching: false,
	assets: 0,
	versions: 0,
	openTickets: 0,
	toReview: 0,
	unreadable: [],
	appVersion: ''
};

// One shared copy of the application status, so the header and the pages agree
// about how many folders are watched without each asking separately.
class StatusStore {
	/** @type {AppStatus} */
	value = $state({ ...empty });

	async refresh() {
		try {
			const next = await Status();
			this.value = { ...empty, ...next };
		} catch {
			// The binding layer is not up yet, or the window is closing. Neither is
			// worth saying anything about: the next tick will either work or the
			// application is gone.
		}
	}
}

export const statusStore = new StatusStore();
