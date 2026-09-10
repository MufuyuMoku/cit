<script>
	import { page } from '$app/state';
	import {
		GetAsset,
		OpenVersion,
		ExportVersion,
		PinVersion,
		DetachFile
	} from '$lib/wailsjs/go/cmd/App.js';
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

	/** @typedef {import('$lib/wailsjs/go/models').cmd.AssetDetail} AssetDetail */

	let asset = $state(/** @type {AssetDetail | null} */ (null));
	let problem = $state('');
	// A one-line outcome for the last thing the user did. It sits in place until
	// they do something else; it never pops up and never steals focus.
	let outcome = $state('');
	let busyVersion = $state(0);
	let busyPath = $state('');

	async function load() {
		try {
			asset = await GetAsset(assetId);
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

	const versions = $derived(asset?.versions ?? []);
	const files = $derived(asset?.files ?? []);
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
						</div>
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
</style>
