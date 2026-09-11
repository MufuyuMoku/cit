<script>
	import '../app.css';
	import { page } from '$app/state';
	import { ChooseFolder, RemoveFolder, RevealDataDir } from '$lib/wailsjs/go/cmd/App.js';
	import { message } from '$lib/format.js';
	import { statusStore } from '$lib/status.svelte.js';

	let { children } = $props();

	let showFolders = $state(false);
	let busy = $state(false);
	let problem = $state('');

	// The status line refreshes on a timer rather than pushing events at the user.
	// Nothing here is urgent enough to interrupt for, and a number that updates
	// quietly is calmer than one that announces itself.
	$effect(() => {
		statusStore.refresh();
		const id = setInterval(() => statusStore.refresh(), 3000);
		return () => clearInterval(id);
	});

	async function chooseFolder() {
		busy = true;
		problem = '';
		try {
			await ChooseFolder();
			await statusStore.refresh();
		} catch (err) {
			problem = message(err);
		} finally {
			busy = false;
		}
	}

	/** @param {string} path */
	async function removeFolder(path) {
		busy = true;
		problem = '';
		try {
			await RemoveFolder(path);
			await statusStore.refresh();
		} catch (err) {
			problem = message(err);
		} finally {
			busy = false;
		}
	}

	const status = $derived(statusStore.value);
</script>

<div class="shell">
	<header>
		<a class="brand" href="/">
			<span class="name">CIT</span>
			<span class="faint version">{status.appVersion}</span>
		</a>

		<nav class="links">
			<a href="/" class:active={page.url.pathname === '/'}>Karya</a>
			<a href="/menunggu" class:active={page.url.pathname === '/menunggu'}>
				Menunggu
				{#if status.openTickets > 0}
					<span class="count">{status.openTickets}</span>
				{/if}
			</a>
			<a href="/tinjauan" class:active={page.url.pathname === '/tinjauan'}>
				Tinjauan
				{#if status.toReview > 0}
					<!-- A plain count, not a badge. Nothing here is an alarm: these are
					     suggestions waiting to be looked at whenever the user feels like
					     it. -->
					<span class="count">{status.toReview}</span>
				{/if}
			</a>
		</nav>

		<div class="spacer"></div>

		<button class="quiet" onclick={() => (showFolders = !showFolders)}>
			{#if status.folders && status.folders.length > 0}
				{status.folders.length} folder diawasi
			{:else}
				Belum ada folder diawasi
			{/if}
		</button>

		<span class="faint watching" title={status.watching ? 'Perubahan sedang diamati' : 'Tidak ada folder untuk diamati'}>
			{#if status.watching}
				<span class="dot on"></span> mengamati
			{:else}
				<span class="dot"></span> diam
			{/if}
		</span>
	</header>

	{#if showFolders}
		<section class="folders">
			<div class="folders-head">
				<h2>Folder yang diawasi</h2>
				<button class="primary" onclick={chooseFolder} disabled={busy}>Tambah folder…</button>
			</div>

			<p class="muted intro">
				CIT mengamati folder ini dan mencatat setiap penyimpanan sebagai satu versi.
				Tidak ada berkas yang dipindahkan atau diubah.
			</p>

			{#if status.folders && status.folders.length > 0}
				<ul>
					{#each status.folders as folder (folder)}
						<li>
							<span class="mono path">{folder}</span>
							<button class="quiet" onclick={() => removeFolder(folder)} disabled={busy}>
								Berhenti mengawasi
							</button>
						</li>
					{/each}
				</ul>
				<p class="faint small">
					Berhenti mengawasi hanya menghentikan pemindaian. Semua karya, versi, dan gambar
					kecil yang sudah tercatat tetap ada.
				</p>
			{:else}
				<p class="muted">Belum ada. Pilih folder kerjamu untuk mulai.</p>
			{/if}

			<div class="where">
				<span class="faint small">Data CIT disimpan di</span>
				<span class="mono path">{status.dataDir}</span>
				<button class="quiet" onclick={() => RevealDataDir()}>Buka folder</button>
			</div>
		</section>
	{/if}

	{#if problem}
		<p class="note shell-note">{problem}</p>
	{/if}
	{#if status.problem}
		<p class="note shell-note">{status.problem}</p>
	{/if}
	{#if status.unreadable && status.unreadable.length > 0}
		<div class="note shell-note">
			<strong>Berkas yang belum bisa dibaca</strong>
			<ul class="plain">
				{#each status.unreadable as line (line)}
					<li class="mono">{line}</li>
				{/each}
			</ul>
		</div>
	{/if}

	<main>
		{@render children()}
	</main>
</div>

<style>
	.shell {
		display: flex;
		flex-direction: column;
		min-height: 100vh;
	}

	header {
		display: flex;
		align-items: center;
		gap: 12px;
		padding: 12px 20px;
		border-bottom: 1px solid var(--line);
		background: var(--surface);
		position: sticky;
		top: 0;
		z-index: 2;
	}

	.brand {
		display: flex;
		align-items: baseline;
		gap: 8px;
	}

	.name {
		font-weight: 600;
		font-size: 16px;
		letter-spacing: 0.02em;
	}

	.version {
		font-size: 11px;
	}

	.spacer {
		flex: 1;
	}

	.links {
		display: flex;
		align-items: center;
		gap: 2px;
		margin-left: 8px;
	}

	.links a {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		padding: 4px 10px;
		border-radius: var(--radius-sm);
		color: var(--fg-muted);
		transition: background 120ms ease, color 120ms ease;
	}

	.links a:hover {
		background: var(--surface-sunken);
		color: var(--fg);
	}

	.links a.active {
		background: var(--accent-soft);
		color: var(--accent);
	}

	/* A count, not a badge: same muted ink as its label, no fill, no red. */
	.count {
		font-size: 11px;
		font-variant-numeric: tabular-nums;
		padding: 0 5px;
		border-radius: 999px;
		border: 1px solid var(--line-strong);
		background: var(--surface);
	}

	.watching {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		font-size: 12px;
	}

	.dot {
		width: 6px;
		height: 6px;
		border-radius: 50%;
		background: var(--line-strong);
	}

	.dot.on {
		background: var(--accent);
	}

	.folders {
		padding: 16px 20px 20px;
		border-bottom: 1px solid var(--line);
		background: var(--surface-sunken);
	}

	.folders-head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 12px;
	}

	.intro {
		margin: 6px 0 12px;
		max-width: 62ch;
	}

	.folders ul {
		list-style: none;
		margin: 0;
		padding: 0;
		display: flex;
		flex-direction: column;
		gap: 4px;
	}

	.folders li {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 12px;
		background: var(--surface);
		border: 1px solid var(--line);
		border-radius: var(--radius-sm);
		padding: 6px 6px 6px 12px;
	}

	.path {
		overflow-wrap: anywhere;
	}

	.small {
		font-size: 12px;
	}

	.where {
		display: flex;
		align-items: center;
		gap: 8px;
		flex-wrap: wrap;
		margin-top: 14px;
		padding-top: 12px;
		border-top: 1px solid var(--line);
	}

	.shell-note {
		margin: 12px 20px 0;
	}

	.plain {
		list-style: none;
		margin: 4px 0 0;
		padding: 0;
	}

	main {
		flex: 1;
		padding: 20px;
	}
</style>
