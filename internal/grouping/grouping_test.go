package grouping

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/MufuyuMoku/cit/internal/store"
)

// --- the seven-file case ----------------------------------------------------

// What one poster actually looks like in a student's folder after a week. Seven
// files, three of the noise words in Indonesian, two in English, one with a
// date, one with a copy counter, one with a version marker — all of it one
// piece of work.
func TestSevenMessyNamesBecomeOneAsset(t *testing.T) {
	f := newFixture(t)

	names := []string{
		"poster-kampus.psd",
		"poster kampus final.psd",
		"poster_kampus_fix.psd",
		"poster kampus revisi 3.psd",
		"Poster Kampus FINAL ASLI.psd",
		"poster_kampus_v2 (1).psd",
		"poster-kampus-20260909.psd",
	}

	var paths []string
	for i, name := range names {
		// Spread across an afternoon, and all visually the same poster with
		// small edits between saves.
		paths = append(paths, f.addFile(name,
			baseTime.Add(time.Duration(i)*40*time.Minute),
			nearlyIdentical(3)))
	}

	if got := f.assetCount(); got != 7 {
		t.Fatalf("prasyarat gagal: %d karya sebelum pengelompokan, mau 7", got)
	}

	res := f.regroup()

	if got := f.assetCount(); got != 1 {
		t.Errorf("%d karya setelah pengelompokan, mau 1: %+v", got, res)
	}
	f.requireSameAsset(paths...)
	f.requireVersionsFollow(paths...)

	if got := f.versionCount(); got != 7 {
		t.Errorf("%d versi, mau 7; pengelompokan tidak boleh menghilangkan versi", got)
	}
	if res.AssetsRemoved != 6 {
		t.Errorf("AssetsRemoved = %d, mau 6", res.AssetsRemoved)
	}
}

// The same seven names, but every one of them without a thumbnail: unknown
// formats, or videos on a machine with no ffmpeg. They must still group.
func TestFilesWithoutThumbnailsStillGroup(t *testing.T) {
	f := newFixture(t)

	names := []string{
		"opening-wisuda.mp4",
		"opening wisuda final.mp4",
		"opening_wisuda_fix.mp4",
		"opening wisuda revisi 2.mp4",
		"Opening Wisuda FIX BANGET.mp4",
	}

	var paths []string
	for i, name := range names {
		paths = append(paths, f.addFile(name,
			baseTime.Add(time.Duration(i)*30*time.Minute), nil))
	}

	res := f.regroup()

	if res.HashesComputed != 0 {
		t.Errorf("HashesComputed = %d; tidak ada gambar kecil untuk di-hash", res.HashesComputed)
	}
	if got := f.assetCount(); got != 1 {
		t.Errorf("%d karya, mau 1; berkas tanpa gambar kecil harus tetap bisa dikelompokkan", got)
	}
	f.requireSameAsset(paths...)
	f.requireVersionsFollow(paths...)
}

// A mixture: some files have thumbnails, some do not. The ones without must not
// be pushed out of the group they belong to.
func TestMixedThumbnailAvailabilityStillGroups(t *testing.T) {
	f := newFixture(t)

	withImage := f.addFile("brosur-klinik.png", baseTime, artwork(5))
	withoutImage := f.addFile("brosur klinik final.capcut", baseTime.Add(20*time.Minute), nil)
	alsoWith := f.addFile("brosur_klinik_fix.png", baseTime.Add(40*time.Minute), nearlyIdentical(5))

	f.regroup()

	f.requireSameAsset(withImage, withoutImage, alsoWith)
}

// --- names alike, pictures not ----------------------------------------------

// Two different logos for two different clients, named the way people name
// things. The names alone would merge them; the pictures must not let that
// happen.
func TestSimilarNamesButVeryDifferentPicturesStayApart(t *testing.T) {
	f := newFixture(t)

	// The pictures must genuinely be far apart, or this test would pass for the
	// wrong reason.
	w := DefaultWeights()
	distance := hammingDistance(phashOf(artwork(1)), phashOf(artwork(9)))
	if distance <= w.DifferentImage {
		t.Fatalf("prasyarat gagal: jarak gambar %d, harus di atas ambang veto %d",
			distance, w.DifferentImage)
	}

	// Same afternoon, so time pushes them together too.
	logoA := f.addFile("logo-warung.png", baseTime, artwork(1))
	logoB := f.addFile("logo warung 2.png", baseTime.Add(15*time.Minute), artwork(9))

	// Without the veto these would merge: the names alone score well above the
	// threshold. That is the whole reason the visual signal is allowed to
	// overrule them.
	a := candidate{normalized: normalizeName(logoA), modifiedAt: baseTime, phash: phashOf(artwork(1))}
	b := candidate{normalized: normalizeName(logoB), modifiedAt: baseTime.Add(15 * time.Minute), phash: phashOf(artwork(9))}
	noVeto := w
	noVeto.DifferentImage = 1 << 20
	if s := scorePair(a, b, noVeto); s.total < w.Threshold {
		t.Fatalf("prasyarat gagal: tanpa veto skornya %.3f, di bawah ambang %.2f; "+
			"uji ini tidak menguji veto", s.total, w.Threshold)
	}

	f.regroup()

	f.requireDifferentAssets(logoA, logoB)
	if got := f.assetCount(); got != 2 {
		t.Errorf("%d karya, mau 2; dua logo berbeda menyatu", got)
	}
	f.requireVersionsFollow(logoA, logoB)
}

// But an identical name after normalisation is not overruled by the picture. A
// radical revision of the same poster is still that poster — that is what a
// revision is.
func TestIdenticalNameSurvivesACompletelyDifferentPicture(t *testing.T) {
	f := newFixture(t)

	// Seeds chosen so the two pictures are further apart than the veto
	// threshold. Without this check the test could quietly stop exercising the
	// identical-name exception if the images ever drifted closer together.
	w := DefaultWeights()
	distance := hammingDistance(phashOf(artwork(3)), phashOf(artwork(11)))
	if distance <= w.DifferentImage {
		t.Fatalf("prasyarat gagal: jarak gambar %d, harus di atas ambang veto %d "+
			"agar uji ini benar-benar menekan pengecualian nama identik",
			distance, w.DifferentImage)
	}

	before := f.addFile("poster-lomba.psd", baseTime, artwork(3))
	after := f.addFile("poster lomba final.psd", baseTime.Add(2*time.Hour), artwork(11))

	f.regroup()

	f.requireSameAsset(before, after)
}

// --- manual decisions are permanent -----------------------------------------

// The requirement that matters most: a split the user made by hand must survive
// every later scan, no matter how strongly the signals disagree.
func TestManualSplitSurvivesTenRegroups(t *testing.T) {
	f := newFixture(t)

	// Two files that automatic grouping would absolutely put together: same
	// normalised name, same minute, same picture.
	a := f.addFile("banner-promo.png", baseTime, nearlyIdentical(4))
	b := f.addFile("banner promo final.png", baseTime.Add(time.Minute), nearlyIdentical(4))

	f.regroup()
	f.requireSameAsset(a, b)

	if err := f.g.Split(t.Context(), a, b); err != nil {
		t.Fatalf("Split: %v", err)
	}
	f.requireDifferentAssets(a, b)

	for i := 0; i < 10; i++ {
		res := f.regroup()
		if res.HonouredApart == 0 {
			t.Errorf("pemindaian %d: HonouredApart = 0; keputusan pengguna tidak terlihat dihormati", i+1)
		}
		f.requireDifferentAssets(a, b)
	}

	// And the history is intact on both sides.
	f.requireVersionsFollow(a, b)
	if got := f.versionCount(); got != 2 {
		t.Errorf("%d versi setelah sepuluh pemindaian, mau 2", got)
	}
}

// Transitivity must not sneak around a split: A joins C, C joins B, so A and B
// end up together despite the user having separated them.
func TestSplitIsNotUndoneThroughAThirdFile(t *testing.T) {
	f := newFixture(t)

	a := f.addFile("katalog-produk.psd", baseTime, nearlyIdentical(6))
	bridge := f.addFile("katalog produk fix.psd", baseTime.Add(10*time.Minute), nearlyIdentical(6))
	b := f.addFile("katalog_produk_final.psd", baseTime.Add(20*time.Minute), nearlyIdentical(6))

	f.regroup()
	f.requireSameAsset(a, bridge, b)

	if err := f.g.Split(t.Context(), a, b); err != nil {
		t.Fatalf("Split: %v", err)
	}
	f.requireDifferentAssets(a, b)

	for i := 0; i < 10; i++ {
		f.regroup()
		f.requireDifferentAssets(a, b)
	}
}

// A merge the user made by hand must equally survive: automatic grouping may
// not pull apart what they joined.
func TestManualMergeSurvivesTenRegroups(t *testing.T) {
	f := newFixture(t)

	// Nothing in common: different names, different pictures, days apart.
	a := f.addFile("undangan-nikah.psd", baseTime, artwork(7))
	b := f.addFile("mockup-kaos.psd", baseTime.Add(72*time.Hour), artwork(15))

	f.regroup()
	f.requireDifferentAssets(a, b)

	if err := f.g.Merge(t.Context(), a, b); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	f.requireSameAsset(a, b)

	for i := 0; i < 10; i++ {
		f.regroup()
		f.requireSameAsset(a, b)
	}
	f.requireVersionsFollow(a, b)
}

func TestDecisionsAreRecordedAndReadable(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()

	a := f.addFile("a.png", baseTime, artwork(1))
	b := f.addFile("b.png", baseTime, artwork(2))

	if err := f.g.Merge(ctx, a, b); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	got, err := store.GroupingDecisionFor(ctx, f.db, a, b)
	if err != nil {
		t.Fatalf("GroupingDecisionFor: %v", err)
	}
	if got.Decision != store.Together {
		t.Errorf("keputusan = %q, mau together", got.Decision)
	}

	// Asked in the other order, the same row comes back.
	reversed, err := store.GroupingDecisionFor(ctx, f.db, b, a)
	if err != nil {
		t.Fatalf("GroupingDecisionFor terbalik: %v", err)
	}
	if reversed.PathA != got.PathA || reversed.PathB != got.PathB {
		t.Error("pasangan yang sama menghasilkan baris berbeda tergantung urutan")
	}

	// A later split replaces it rather than adding a second row.
	if err := f.g.Split(ctx, a, b); err != nil {
		t.Fatalf("Split: %v", err)
	}
	got, err = store.GroupingDecisionFor(ctx, f.db, a, b)
	if err != nil {
		t.Fatalf("GroupingDecisionFor setelah split: %v", err)
	}
	if got.Decision != store.Apart {
		t.Errorf("keputusan = %q setelah split, mau apart", got.Decision)
	}

	all, err := store.AllGroupingDecisions(ctx, f.db)
	if err != nil {
		t.Fatalf("AllGroupingDecisions: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("%d keputusan tercatat, mau 1", len(all))
	}
}

// --- splitting and merging keep the history ---------------------------------

// Tickets in M6 will hang off versions, and versions hang off assets. Splitting
// must move the versions with the file, or those tickets end up pointing at the
// wrong work and the timeline the user sees loses entries.
func TestSplitMovesVersionsWithTheFile(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()

	a := f.addFile("feed-ig.png", baseTime, nearlyIdentical(8))
	b := f.addFile("feed ig fix.png", baseTime.Add(30*time.Minute), nearlyIdentical(8))

	f.regroup()
	merged := f.requireSameAsset(a, b)

	// Two more saves of b, so it has a history of its own to carry.
	for i := 0; i < 2; i++ {
		if _, err := store.AddVersion(ctx, f.db, store.Version{
			AssetID:    merged,
			FileHash:   "extra" + string(rune('a'+i)),
			Size:       2048,
			ObservedAt: baseTime.Add(time.Duration(i+1) * time.Hour),
			ModifiedAt: baseTime.Add(time.Duration(i+1) * time.Hour),
			SourcePath: b,
			FileKey:    b,
		}); err != nil {
			t.Fatalf("AddVersion: %v", err)
		}
	}

	before := f.versionCount()

	if err := f.g.Split(ctx, a, b); err != nil {
		t.Fatalf("Split: %v", err)
	}

	if got := f.versionCount(); got != before {
		t.Errorf("%d versi setelah pisah, mau tetap %d; memisahkan tidak boleh menghilangkan versi",
			got, before)
	}
	f.requireVersionsFollow(a, b)

	// b now owns three versions on its own asset.
	versions, err := store.VersionsByAsset(ctx, f.db, f.assetOf(b))
	if err != nil {
		t.Fatalf("VersionsByAsset: %v", err)
	}
	if len(versions) != 3 {
		t.Errorf("%d versi di karya b, mau 3", len(versions))
	}
}

func TestMergeKeepsEveryVersion(t *testing.T) {
	f := newFixture(t)

	a := f.addFile("sertifikat.psd", baseTime, artwork(12))
	b := f.addFile("piagam.psd", baseTime.Add(48*time.Hour), artwork(20))

	before := f.versionCount()

	if err := f.g.Merge(t.Context(), a, b); err != nil {
		t.Fatalf("Merge: %v", err)
	}

	if got := f.versionCount(); got != before {
		t.Errorf("%d versi setelah gabung, mau tetap %d", got, before)
	}
	f.requireSameAsset(a, b)
	f.requireVersionsFollow(a, b)
	if got := f.assetCount(); got != 1 {
		t.Errorf("%d karya setelah gabung, mau 1", got)
	}
}

// Splitting a file out of a group of three leaves the other two alone.
func TestSplitOnlyMovesTheFileNamed(t *testing.T) {
	f := newFixture(t)

	a := f.addFile("spanduk-bazar.psd", baseTime, nearlyIdentical(13))
	b := f.addFile("spanduk bazar fix.psd", baseTime.Add(10*time.Minute), nearlyIdentical(13))
	c := f.addFile("spanduk_bazar_final.psd", baseTime.Add(20*time.Minute), nearlyIdentical(13))

	f.regroup()
	f.requireSameAsset(a, b, c)

	if err := f.g.Split(t.Context(), a, c); err != nil {
		t.Fatalf("Split: %v", err)
	}

	f.requireSameAsset(a, b)
	f.requireDifferentAssets(a, c)
	f.requireVersionsFollow(a, b, c)
}

// --- guard rails ------------------------------------------------------------

func TestSplitAndMergeRejectUnknownPaths(t *testing.T) {
	f := newFixture(t)
	real := f.addFile("ada.png", baseTime, artwork(1))
	missing := f.path("tidak-ada.png")

	if err := f.g.Split(t.Context(), real, missing); !errors.Is(err, ErrUnknownPath) {
		t.Errorf("Split jalur tak dikenal = %v, mau ErrUnknownPath", err)
	}
	if err := f.g.Merge(t.Context(), missing, real); !errors.Is(err, ErrUnknownPath) {
		t.Errorf("Merge jalur tak dikenal = %v, mau ErrUnknownPath", err)
	}
}

func TestRegroupIsIdempotent(t *testing.T) {
	f := newFixture(t)

	names := []string{"desain-menu.psd", "desain menu fix.psd", "desain_menu_final.psd"}
	var paths []string
	for i, n := range names {
		paths = append(paths, f.addFile(n, baseTime.Add(time.Duration(i)*time.Hour), nearlyIdentical(14)))
	}

	first := f.regroup()
	assetID := f.requireSameAsset(paths...)

	second := f.regroup()
	if second.Moved != 0 {
		t.Errorf("pemindaian kedua memindahkan %d berkas; seharusnya nol", second.Moved)
	}
	if second.AssetsRemoved != 0 {
		t.Errorf("pemindaian kedua menghapus %d karya; seharusnya nol", second.AssetsRemoved)
	}
	if got := f.requireSameAsset(paths...); got != assetID {
		t.Errorf("karya berubah dari %d jadi %d tanpa alasan", assetID, got)
	}
	if first.Groups != second.Groups {
		t.Errorf("jumlah kelompok berubah %d -> %d", first.Groups, second.Groups)
	}
}

func TestRegroupOnEmptyDatabase(t *testing.T) {
	f := newFixture(t)

	res := f.regroup()
	if res.Files != 0 || res.Groups != 0 || res.Moved != 0 {
		t.Errorf("basis data kosong menghasilkan %+v", res)
	}
}

// Two genuinely unrelated works stay apart, which is the baseline the whole
// thing rests on.
func TestUnrelatedWorksStayApart(t *testing.T) {
	f := newFixture(t)

	a := f.addFile("poster-konser.psd", baseTime, artwork(21))
	b := f.addFile("logo-kopi.png", baseTime.Add(96*time.Hour), artwork(30))
	c := f.addFile("kartu-nama.ai", baseTime.Add(200*time.Hour), nil)

	f.regroup()

	if got := f.assetCount(); got != 3 {
		t.Errorf("%d karya, mau 3; karya tak berhubungan disatukan", got)
	}
	f.requireDifferentAssets(a, b)
	f.requireDifferentAssets(b, c)
	f.requireVersionsFollow(a, b, c)
}

// The perceptual hash is computed once and reused.
func TestPerceptualHashIsComputedOnceAndStored(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()

	path := f.addFile("karya.png", baseTime, artwork(3))

	first := f.regroup()
	if first.HashesComputed != 1 {
		t.Errorf("HashesComputed = %d pada pemindaian pertama, mau 1", first.HashesComputed)
	}

	row, err := store.ObservedFileByPath(ctx, f.db, path)
	if err != nil {
		t.Fatalf("ObservedFileByPath: %v", err)
	}
	phash, err := store.PreviewPHash(ctx, f.db, row.LastHash)
	if err != nil {
		t.Fatalf("PreviewPHash: %v", err)
	}
	if len(phash) != 16 {
		t.Errorf("phash %q panjangnya %d, mau 16 karakter heks", phash, len(phash))
	}

	second := f.regroup()
	if second.HashesComputed != 0 {
		t.Errorf("HashesComputed = %d pada pemindaian kedua; seharusnya dipakai ulang", second.HashesComputed)
	}
}

// A thumbnail that will not decode is a missing signal, not a reason to stop.
func TestUndecodableThumbnailDoesNotStopGrouping(t *testing.T) {
	f := newFixture(t)

	a := f.addFile("kalender.psd", baseTime, artwork(17))
	b := f.addFile("kalender fix.psd", baseTime.Add(time.Hour), artwork(17))

	// Corrupt one of the stored thumbnails.
	row, err := store.ObservedFileByPath(t.Context(), f.db, b)
	if err != nil {
		t.Fatalf("ObservedFileByPath: %v", err)
	}
	f.thumbs.data[row.LastHash] = []byte("ini jelas bukan gambar")

	res := f.regroup()
	if res.Files != 2 {
		t.Fatalf("Files = %d, mau 2", res.Files)
	}

	// Name and time carry it: they still group.
	f.requireSameAsset(a, b)
}

func TestGrouperWithoutThumbnailsWorks(t *testing.T) {
	f := newFixture(t)
	f.g = New(f.db, nil, WithClock(func() time.Time { return baseTime }))

	a := f.addFile("agenda-rapat.psd", baseTime, artwork(1))
	b := f.addFile("agenda rapat final.psd", baseTime.Add(time.Hour), artwork(25))

	res := f.regroup()
	if res.HashesComputed != 0 {
		t.Errorf("HashesComputed = %d tanpa sumber gambar kecil", res.HashesComputed)
	}
	// Without pictures, the names decide, and these two names agree.
	f.requireSameAsset(a, b)
}

// Grouping decisions follow a file when ingest renames it.
func TestDecisionsFollowARename(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()

	a := f.addFile("draft-1.png", baseTime, nearlyIdentical(19))
	b := f.addFile("draft 1 fix.png", baseTime.Add(time.Hour), nearlyIdentical(19))

	f.regroup()
	if err := f.g.Split(ctx, a, b); err != nil {
		t.Fatalf("Split: %v", err)
	}

	// Ingest would do exactly this when it recognises a rename.
	renamed := f.path("draft-1-terbaru.png")
	if _, err := f.db.ExecContext(ctx,
		`UPDATE observed_files SET path = ? WHERE path = ?`, renamed, b); err != nil {
		t.Fatalf("ganti nama jalur: %v", err)
	}
	if err := store.RenameVersionFileKey(ctx, f.db, b, renamed); err != nil {
		t.Fatalf("RenameVersionFileKey: %v", err)
	}
	if err := store.RenameGroupingDecisions(ctx, f.db, b, renamed); err != nil {
		t.Fatalf("RenameGroupingDecisions: %v", err)
	}

	for i := 0; i < 5; i++ {
		f.regroup()
		f.requireDifferentAssets(a, renamed)
	}

	if _, err := store.GroupingDecisionFor(ctx, f.db, a, renamed); err != nil {
		t.Errorf("keputusan tidak ikut pindah saat berkas diganti nama: %v", err)
	}
}

func TestVersionsNeverDisappearAcrossManyOperations(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()

	var paths []string
	for i, n := range []string{
		"proyek-akhir.psd", "proyek akhir fix.psd", "proyek_akhir_final.psd",
		"proyek akhir revisi 2.psd", "Proyek Akhir ASLI.psd",
	} {
		paths = append(paths, f.addFile(n, baseTime.Add(time.Duration(i)*time.Hour), nearlyIdentical(22)))
	}
	want := f.versionCount()

	f.regroup()
	if err := f.g.Split(ctx, paths[0], paths[3]); err != nil {
		t.Fatalf("Split: %v", err)
	}
	f.regroup()
	if err := f.g.Merge(ctx, paths[1], paths[4]); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	f.regroup()
	if err := f.g.Split(ctx, paths[1], paths[2]); err != nil {
		t.Fatalf("Split: %v", err)
	}
	for i := 0; i < 5; i++ {
		f.regroup()
	}

	if got := f.versionCount(); got != want {
		t.Errorf("%d versi setelah rangkaian operasi, mau tetap %d", got, want)
	}
	f.requireVersionsFollow(paths...)

	// Every path still belongs to some asset, and every asset still exists.
	for _, p := range paths {
		if id := f.assetOf(p); id == 0 {
			t.Errorf("%s kehilangan karyanya", filepath.Base(p))
		}
	}
}
