<script>
	import { WaitingOnThem, WaitingOnMe, CloseTicket } from '$lib/wailsjs/go/cmd/App.js';
	import { message } from '$lib/format.js';
	import TicketList from '$lib/TicketList.svelte';

	/** @typedef {import('$lib/wailsjs/go/models').cmd.TicketView} TicketView */

	let theirs = $state(/** @type {TicketView[]} */ ([]));
	let mine = $state(/** @type {TicketView[]} */ ([]));
	let loaded = $state(false);
	let problem = $state('');
	let busy = $state(0);

	async function load() {
		try {
			const [a, b] = await Promise.all([WaitingOnThem(), WaitingOnMe()]);
			theirs = a ?? [];
			mine = b ?? [];
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
</script>

<h1>Yang sedang ditunggu</h1>

{#if problem}
	<p class="note">{problem}</p>
{/if}

<!-- Two directions, kept apart on purpose: one is a question of following
     something up, the other is a question of sitting down and working. -->
<section>
	<header class="section-head">
		<h2>Aku menunggu orang lain</h2>
		{#if theirs.length > 0}
			<span class="faint">{theirs.length}</span>
		{/if}
	</header>

	{#if theirs.length > 0}
		<TicketList tickets={theirs} showAge={true} showAsset={true} {busy} onClose={close} />
	{:else if loaded}
		<p class="muted">Tidak ada yang sedang kamu tunggu dari siapa pun.</p>
	{/if}
</section>

<section>
	<header class="section-head">
		<h2>Orang lain menunggu aku</h2>
		{#if mine.length > 0}
			<span class="faint">{mine.length}</span>
		{/if}
	</header>

	{#if mine.length > 0}
		<p class="muted intro">Utang pekerjaan.</p>
		<TicketList tickets={mine} showAsset={true} {busy} onClose={close} />
	{:else if loaded}
		<p class="muted">Tidak ada pekerjaan yang sedang ditunggu orang lain.</p>
	{/if}
</section>

<style>
	h1 {
		margin-bottom: 20px;
	}

	section {
		margin-bottom: 30px;
	}

	.section-head {
		display: flex;
		align-items: baseline;
		gap: 10px;
		margin-bottom: 10px;
	}

	.intro {
		margin: 0 0 10px;
	}
</style>
