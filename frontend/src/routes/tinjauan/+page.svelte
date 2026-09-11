<script>
	import { ReviewInbox, CloseTicket, ReopenTicket } from '$lib/wailsjs/go/cmd/App.js';
	import { message } from '$lib/format.js';
	import TicketList from '$lib/TicketList.svelte';

	/** @typedef {import('$lib/wailsjs/go/models').cmd.TicketView} TicketView */

	let tickets = $state(/** @type {TicketView[]} */ ([]));
	let loaded = $state(false);
	let problem = $state('');
	let busy = $state(0);

	async function load() {
		try {
			tickets = (await ReviewInbox()) ?? [];
			problem = '';
		} catch (err) {
			problem = message(err);
		} finally {
			loaded = true;
		}
	}

	$effect(() => {
		load();
		const id = setInterval(load, 3000);
		return () => clearInterval(id);
	});

	/** @param {number} id */
	async function close(id) {
		busy = id;
		problem = '';
		try {
			await CloseTicket(id);
			await load();
		} catch (err) {
			problem = message(err);
		} finally {
			busy = 0;
		}
	}

	/** @param {number} id */
	async function reopen(id) {
		busy = id;
		problem = '';
		try {
			await ReopenTicket(id);
			await load();
		} catch (err) {
			problem = message(err);
		} finally {
			busy = 0;
		}
	}
</script>

<header class="head">
	<h1>Kotak tinjauan</h1>
	{#if tickets.length > 0}
		<span class="faint">{tickets.length} hal menunggu keputusanmu</span>
	{/if}
</header>

{#if problem}
	<p class="note">{problem}</p>
{/if}

{#if loaded && tickets.length === 0}
	<section class="empty">
		<h2>Tidak ada yang perlu ditinjau</h2>
		<p class="muted">
			Kalau ada versi baru muncul pada karya yang masih punya tiket terbuka, tiket itu
			pindah ke sini. CIT tidak pernah menutupnya sendiri dan tidak pernah memberi tahu
			dengan menyela — halaman ini menunggu sampai kamu membukanya.
		</p>
	</section>
{:else if tickets.length > 0}
	<p class="muted intro">
		Versi baru sudah muncul pada karya-karya ini, jadi tiketnya mungkin sudah beres.
		CIT tidak memutuskan itu — kamu yang memutuskan.
	</p>

	<TicketList {tickets} showAsset={true} {busy} onClose={close} onReopen={reopen} />
{/if}

<style>
	.head {
		display: flex;
		align-items: baseline;
		gap: 12px;
		margin-bottom: 12px;
	}

	.intro {
		margin: 0 0 14px;
		max-width: 72ch;
	}

	.empty {
		max-width: 62ch;
		margin: 8vh auto 0;
		text-align: center;
	}

	.empty h2 {
		margin-bottom: 10px;
	}
</style>
