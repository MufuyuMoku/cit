<script>
	import { page } from '$app/state';
	import {
		GetAsset,
		OpenVersion,
		ExportVersion,
		PinVersion,
		DetachFile,
		TicketsForAsset,
		OpenTicket,
		CloseTicket,
		ReopenTicket
	} from '$lib/wailsjs/go/cmd/App.js';
	import TicketList from '$lib/TicketList.svelte';
	import {
		formatDateTime,
		formatRelative,
		formatSize,
		message,
		count,
		baseName,
		dirName
	} from '$lib/format.js';

	const assetId = $derived(Number(page.params.id));

	/**
	 * @typedef {import('$lib/wailsjs/go/models').cmd.AssetDetail} AssetDetail
	 * @typedef {import('$lib/wailsjs/go/models').cmd.TicketView} TicketView
	 */

	let asset = $state(/** @type {AssetDetail | null} */ (null));
	let tickets = $state(/** @type {TicketView[]} */ ([]));

	// Which version a new ticket is being written against, 0 when the form is
	// closed. A ticket is about one save, so it is opened from a timeline entry
	// rather than from the work as a whole.
	let ticketFor = $state(0);
	let ticketDirection = $state('waiting_on_them');
	let ticketNote = $state('');
	let ticketWho = $state('');
	let busyTicket = $state(0);
	let problem = $state('');
	// A one-line outcome for the last thing the user did. It sits in place until
	// they do something else; it never pops up and never steals focus.
	let outcome = $state('');
	let busyVersion = $state(0);
	let busyPath = $state('');

	async function load() {
		try {
			const [detail, list] = await Promise.all([GetAsset(assetId), TicketsForAsset(assetId)]);
			asset = detail;
			tickets = list ?? [];
			problem = '';
		} catch (err) {
			problem = message(err);
		}
	}

	$effect(() => {
		load();
		const id = setInterval(load, 2500);
		return () => clearInterval(id);
	});

	/** @param {number} versionId */
	async function open(versionId) {
		busyVersion = versionId;
		problem = '';
		outcome = '';
		try {
			const note = await OpenVersion(versionId);
			outcome = note || 'Dibuka di aplikasi asalnya.';
		} catch (err) {
			problem = message(err);
		} finally {
			busyVersion = 0;
		}
	}

	/** @param {number} versionId */
	async function exportVersion(versionId) {
		busyVersion = versionId;
		problem = '';
		outcome = '';
		try {
			const dest = await ExportVersion(versionId);
			outcome = dest ? `Disimpan ke ${dest}` : '';
		} catch (err) {
			// Export failures are the ones that must be said plainly: a corrupt
			// chunk means no file was written at all, and the user needs to know
			// which version that was.
			problem = message(err);
		} finally {
			busyVersion = 0;
		}
	}

	/** @param {number} versionId @param {boolean} pinned */
	async function pin(versionId, pinned) {
		busyVersion = versionId;
		problem = '';
		outcome = '';
		try {
			await PinVersion(versionId, pinned);
			await load();
			outcome = pinned
				? 'Versi ini ditandai: pemangkasan tidak akan pernah membuangnya.'
				: 'Tanda dilepas. Versi ini bisa ikut dipangkas nanti.';
		} catch (err) {
			problem = message(err);
		} finally {
			busyVersion = 0;
		}
	}

	/** @param {string} path */
	async function detach(path) {
		busyPath = path;
		problem = '';
		outcome = '';
		try {
			await DetachFile(path);
			await load();
			outcome = `${baseName(path)} dipisahkan jadi karya sendiri. Pengelompokan otomatis tidak akan menggabungkannya lagi.`;
		} catch (err) {
			problem = message(err);
		} finally {
			busyPath = '';
		}
	}

	/** @param {number} versionId */
	function startTicket(versionId) {
		ticketFor = ticketFor === versionId ? 0 : versionId;
		ticketNote = '';
		ticketWho = '';
		ticketDirection = 'waiting_on_them';
		outcome = '';
		problem = '';
	}

	async function saveTicket() {
		busyTicket = ticketFor;
		problem = '';
		outcome = '';
		try {
			await OpenTicket(ticketFor, ticketDirection, ticketNote, ticketWho);
			ticketFor = 0;
			await load();
			outcome = 'Tiket dicatat. Kalau nanti ada versi baru pada karya ini, tiketnya pindah ke kotak tinjauan — tidak ditutup sendiri.';
		} catch (err) {
			problem = message(err);
		} finally {
			busyTicket = 0;
		}
	}

	/** @param {number} id */
	async function closeTicket(id) {
		busyTicket = id;
		problem = '';
		outcome = '';
		try {
			await CloseTicket(id);
			await load();
		} catch (err) {
			problem = message(err);
		} finally {
			busyTicket = 0;
		}
	}

	/** @param {number} id */
	async function reopenTicket(id) {
		busyTicket = id;
		problem = '';
		outcome = '';
		try {
			await ReopenTicket(id);
			await load();
		} catch (err) {
			problem = message(err);
		} finally {
			busyTicket = 0;
		}
	}

	const versions = $derived(asset?.versions ?? []);
	const files = $derived(asset?.files ?? []);
	const liveTickets = $derived(tickets.filter((t) => t.status !== 'closed'));
	const doneTickets = $derived(tickets.filter((t) => t.status === 'closed'));

	/** @param {number} versionId */
	function ticketsOn(versionId) {
		return liveTickets.filter((t) => t.versionId === versionId);
	}
</script>

<nav class="back">
	<a href="/"><button class="quiet">← Semua karya</button></a>
</nav>

{#if problem}
	<p class="note">{problem}</p>
{/if}
{#if outcome}
	<p class="note">{outcome}</p>
{/if}

{#if asset}
	<header class="asset-head">
		<h1>{asset.name}</h1>
		<span class="faint">
			{count(versions.length, 'versi')} · {count(files.length, 'berkas')}
		</span>
	</header>

	<!-- Which files on disk make up this work, and when each was last really
	     read rather than assumed unchanged. -->
	<section class="files">
		<h2>Berkas</h2>
		{#if files.length > 1}
			<p class="muted intro">
				CIT menduga berkas-berkas ini adalah satu karya yang sama. Kalau dugaannya salah,
				pisahkan — keputusanmu permanen dan tidak akan pernah dibatalkan oleh pemindaian
				berikutnya.
			</p>
		{/if}

		<ul>
			{#each files as file (file.path)}
				<li>
					<div class="file-main">
						<span class="file-name">{baseName(file.path)}</span>
						<span class="mono faint folder">{dirName(file.path)}</span>
					</div>

					<div class="file-side">
						{#if !file.exists}
							<span class="tag set-aside">tidak ada di disk</span>
						{/if}
						<span
							class="faint verified"
							title={file.lastVerifiedAt
								? `Isi berkas terakhir benar-benar dibaca dan di-hash pada ${formatDateTime(file.lastVerifiedAt)}`
								: 'Isi berkas ini belum pernah diverifikasi ulang sejak dicatat'}
						>
							{#if file.lastVerifiedAt}
								diperiksa {formatRelative(file.lastVerifiedAt)}
							{:else}
								belum pernah diperiksa ulang
							{/if}
						</span>

						{#if files.length > 1}
							<button
								class="quiet"
								onclick={() => detach(file.path)}
								disabled={busyPath === file.path}
							>
								Pisahkan dari karya ini
							</button>
						{/if}
					</div>
				</li>
			{/each}
		</ul>
	</section>

	<section class="tickets-section">
		<h2>Tiket</h2>
		{#if liveTickets.length > 0}
			<TicketList
				tickets={liveTickets}
				showAge={true}
				busy={busyTicket}
				onClose={closeTicket}
				onReopen={reopenTicket}
			/>
		{:else}
			<p class="muted">
				Belum ada tiket. Tiket dibuat dari sebuah versi di linimasa di bawah — tiket
				selalu menempel pada satu penyimpanan, bukan pada karya secara umum.
			</p>
		{/if}

		{#if doneTickets.length > 0}
			<details class="done">
				<summary class="faint">{doneTickets.length} tiket yang sudah selesai</summary>
				<TicketList tickets={doneTickets} busy={busyTicket} onReopen={reopenTicket} />
			</details>
		{/if}
	</section>

	<section class="timeline">
		<h2>Linimasa</h2>
		<ul>
			{#each versions as version, i (version.id)}
				<li class:gone={!version.contentPresent}>
					<div class="thumb">
						{#if version.thumbUrl}
							<img src={version.thumbUrl} alt="" loading="lazy" />
						{:else}
							<div class="no-thumb faint">—</div>
						{/if}
					</div>

					<div class="body">
						<div class="line-one">
							<span class="when">{formatDateTime(version.observedAt)}</span>
							{#if i === 0}
								<span class="tag kept">terbaru</span>
							{/if}
							{#if version.pinned}
								<span class="tag kept">ditandai, kebal pemangkasan</span>
							{/if}
							{#if !version.contentPresent}
								<!-- The timeline may never have a hole in it. A version whose
								     bytes retention has discarded still appears, and says so
								     rather than pretending it can still be opened. -->
								<span class="tag set-aside">berkasnya tidak lagi disimpan</span>
							{/if}
							{#if version.alphaFlattened}
								<span class="tag set-aside">transparansi diratakan</span>
							{/if}
							{#each ticketsOn(version.id) as t (t.id)}
								<!-- An open ticket makes this version immune from thinning, so say
								     so where the user is looking at the version. -->
								<span class="tag kept">
									{t.direction === 'waiting_on_them' ? 'menunggu' : 'dikerjakan'}, kebal pemangkasan
								</span>
							{/each}
						</div>

						<div class="line-two faint">
							{formatSize(version.size)} · {version.fileName} ·
							<span class="mono">{version.hash}</span>
							{#if version.contentReleasedAt}
								· isinya dibuang {formatRelative(version.contentReleasedAt)}
							{/if}
						</div>

						{#if version.previewNote}
							<div class="faint preview-note">{version.previewNote}</div>
						{/if}

						<div class="actions">
							<button
								onclick={() => open(version.id)}
								disabled={busyVersion === version.id || !version.contentPresent}
							>
								Buka
							</button>
							<button
								onclick={() => exportVersion(version.id)}
								disabled={busyVersion === version.id || !version.contentPresent}
							>
								Ekspor…
							</button>
							<button
								class="quiet"
								onclick={() => pin(version.id, !version.pinned)}
								disabled={busyVersion === version.id}
							>
								{version.pinned ? 'Lepas tanda' : 'Tandai'}
							</button>
							<button class="quiet" onclick={() => startTicket(version.id)}>
								{ticketFor === version.id ? 'Batal' : 'Buat tiket…'}
							</button>
						</div>

						{#if ticketFor === version.id}
							<form
								class="ticket-form"
								onsubmit={(e) => {
									e.preventDefault();
									saveTicket();
								}}
							>
								<div class="row">
									<label>
										<input type="radio" bind:group={ticketDirection} value="waiting_on_them" />
										Aku menunggu orang lain
									</label>
									<label>
										<input type="radio" bind:group={ticketDirection} value="waiting_on_me" />
										Orang lain menunggu aku
									</label>
								</div>

								<input
									class="note-input"
									type="text"
									bind:value={ticketNote}
									placeholder="Apa yang ditunggu? Misalnya: menunggu teks final dari klien"
								/>
								<input
									class="who-input"
									type="text"
									bind:value={ticketWho}
									placeholder={ticketDirection === 'waiting_on_them'
										? 'Menunggu siapa? (opsional)'
										: 'Untuk siapa? (opsional)'}
								/>

								<div class="row">
									<button class="primary" type="submit" disabled={busyTicket === version.id}>
										Simpan tiket
									</button>
									<span class="faint hint">
										Tiket ini menempel pada versi ini, dan membuatnya kebal pemangkasan
										selama masih terbuka.
									</span>
								</div>
							</form>
						{/if}
					</div>
				</li>
			{/each}
		</ul>
	</section>
{/if}

<style>
	.back {
		margin-bottom: 8px;
	}

	.asset-head {
		display: flex;
		align-items: baseline;
		gap: 12px;
		margin-bottom: 20px;
	}

	section {
		margin-bottom: 28px;
	}

	section h2 {
		margin-bottom: 8px;
	}

	.intro {
		margin: 0 0 10px;
		max-width: 70ch;
	}

	.files ul,
	.timeline ul {
		list-style: none;
		margin: 0;
		padding: 0;
		display: flex;
		flex-direction: column;
		gap: 8px;
	}

	.files li {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 16px;
		flex-wrap: wrap;
		background: var(--surface);
		border: 1px solid var(--line);
		border-radius: var(--radius-sm);
		padding: 8px 8px 8px 12px;
	}

	.file-main {
		display: flex;
		flex-direction: column;
		min-width: 0;
	}

	.file-name {
		font-weight: 500;
		overflow-wrap: anywhere;
	}

	.folder {
		overflow-wrap: anywhere;
	}

	.file-side {
		display: flex;
		align-items: center;
		gap: 10px;
		flex-wrap: wrap;
	}

	.verified {
		font-size: 12px;
	}

	.timeline li {
		display: flex;
		gap: 14px;
		background: var(--surface);
		border: 1px solid var(--line);
		border-radius: var(--radius);
		padding: 12px;
		box-shadow: var(--shadow);
	}

	/* A version whose content is gone is dimmed, not hidden. It is still part of
	 * the history and still says when it was made. */
	.timeline li.gone .thumb {
		opacity: 0.55;
	}

	.thumb {
		flex: 0 0 112px;
		width: 112px;
		height: 84px;
		background: var(--surface-sunken);
		border-radius: var(--radius-sm);
		display: flex;
		align-items: center;
		justify-content: center;
		overflow: hidden;
	}

	.thumb img {
		width: 100%;
		height: 100%;
		object-fit: contain;
	}

	.body {
		flex: 1;
		min-width: 0;
		display: flex;
		flex-direction: column;
		gap: 4px;
	}

	.line-one {
		display: flex;
		align-items: center;
		gap: 8px;
		flex-wrap: wrap;
	}

	.when {
		font-weight: 500;
	}

	.line-two {
		font-size: 12px;
		overflow-wrap: anywhere;
	}

	.preview-note {
		font-size: 12px;
		max-width: 70ch;
	}

	.actions {
		display: flex;
		gap: 8px;
		margin-top: 6px;
		flex-wrap: wrap;
	}

	.done {
		margin-top: 10px;
	}

	.done summary {
		cursor: pointer;
		font-size: 12px;
		padding: 4px 0;
	}

	.ticket-form {
		display: flex;
		flex-direction: column;
		gap: 8px;
		margin-top: 10px;
		padding: 12px;
		background: var(--surface-sunken);
		border: 1px solid var(--line);
		border-radius: var(--radius-sm);
	}

	.ticket-form .row {
		display: flex;
		align-items: center;
		gap: 14px;
		flex-wrap: wrap;
	}

	.ticket-form label {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		cursor: pointer;
	}

	.ticket-form input[type='text'] {
		font: inherit;
		color: inherit;
		padding: 7px 10px;
		background: var(--surface);
		border: 1px solid var(--line-strong);
		border-radius: var(--radius-sm);
		width: 100%;
	}

	.ticket-form input[type='text']:focus {
		outline: 2px solid var(--accent-soft);
		border-color: var(--accent);
	}

	.hint {
		font-size: 12px;
		max-width: 52ch;
	}
</style>
