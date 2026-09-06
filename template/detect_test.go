package template

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// DetectProjectType uses the same file-based heuristics as cmd/init.go.
// Centralising here lets us test detection without cobra.
func DetectProjectType(dir string) (isNode, isPython, isKotlin, isJava bool) {
	stat := func(name string) bool {
		_, err := os.Stat(filepath.Join(dir, name))
		return err == nil
	}
	readContains := func(name, substr string) bool {
		data, err := os.ReadFile(filepath.Join(dir, name))
		return err == nil && len(data) > 0 && contains(string(data), substr)
	}
	glob := func(pattern string) bool {
		m, _ := filepath.Glob(filepath.Join(dir, pattern))
		return len(m) > 0
	}

	isNode = stat("package.json")

	if slices.ContainsFunc([]string{"requirements.txt", "pyproject.toml", "setup.py"}, stat) {
		isPython = true
	}

	switch {
	case stat("build.gradle.kts"), stat("settings.gradle.kts"):
		isKotlin = true
	case readContains("build.gradle", "kotlin"):
		isKotlin = true
	case readContains("pom.xml", "kotlin"):
		isKotlin = true
	case glob("src/**/*.kt"), glob("src/*.kt"):
		isKotlin = true
	}

	if !isKotlin {
		switch {
		case stat("pom.xml"):
			isJava = true
		case stat("build.gradle"):
			isJava = true
		case glob("src/**/*.java"), glob("src/*.java"):
			isJava = true
		}
	}
	return
}

// DetectProjectTypeAll extends DetectProjectType with Rust detection.
func DetectProjectTypeAll(dir string) (isNode, isPython, isKotlin, isJava, isRust bool) {
	isNode, isPython, isKotlin, isJava = DetectProjectType(dir)

	stat := func(name string) bool {
		_, err := os.Stat(filepath.Join(dir, name))
		return err == nil
	}
	glob := func(pattern string) bool {
		m, _ := filepath.Glob(filepath.Join(dir, pattern))
		return len(m) > 0
	}

	switch {
	case stat("Cargo.toml"):
		isRust = true
	case glob("src/**/*.rs"), glob("src/*.rs"):
		isRust = true
	}
	return
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}

// ── tests ─────────────────────────────────────────────────────────────────────

func TestDetectProjectType_Node(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"app"}`), 0644)
	node, py, kt, java := DetectProjectType(dir)
	if !node {
		t.Error("should detect Node")
	}
	if py || kt || java {
		t.Errorf("unexpected: py=%v kt=%v java=%v", py, kt, java)
	}
}

func TestDetectProjectType_Python_Requirements(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "requirements.txt"), []byte("asyncpg\n"), 0644)
	_, py, kt, java := DetectProjectType(dir)
	if !py {
		t.Error("should detect Python via requirements.txt")
	}
	if kt || java {
		t.Errorf("unexpected: kt=%v java=%v", kt, java)
	}
}

func TestDetectProjectType_Python_Pyproject(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte("[build-system]\n"), 0644)
	_, py, _, _ := DetectProjectType(dir)
	if !py {
		t.Error("should detect Python via pyproject.toml")
	}
}

func TestDetectProjectType_Python_SetupPy(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "setup.py"), []byte("from setuptools import setup\n"), 0644)
	_, py, _, _ := DetectProjectType(dir)
	if !py {
		t.Error("should detect Python via setup.py")
	}
}

func TestDetectProjectType_Kotlin_GradleKts(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "build.gradle.kts"), []byte(`plugins { kotlin("jvm") }`), 0644)
	_, _, kt, java := DetectProjectType(dir)
	if !kt {
		t.Error("should detect Kotlin via build.gradle.kts")
	}
	if java {
		t.Error("Kotlin project must not be identified as Java")
	}
}

func TestDetectProjectType_Kotlin_SettingsKts(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "settings.gradle.kts"), []byte(`rootProject.name = "app"`), 0644)
	_, _, kt, _ := DetectProjectType(dir)
	if !kt {
		t.Error("should detect Kotlin via settings.gradle.kts")
	}
}

func TestDetectProjectType_Kotlin_GradleGroovyWithKotlin(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "build.gradle"), []byte(`apply plugin: 'kotlin'`), 0644)
	_, _, kt, java := DetectProjectType(dir)
	if !kt {
		t.Error("should detect Kotlin via build.gradle with kotlin keyword")
	}
	if java {
		t.Error("should not also detect Java")
	}
}

func TestDetectProjectType_Kotlin_PomWithKotlin(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "pom.xml"), []byte(`<dependencies><groupId>org.jetbrains.kotlin</groupId></dependencies>`), 0644)
	_, _, kt, java := DetectProjectType(dir)
	if !kt {
		t.Error("should detect Kotlin via pom.xml with kotlin")
	}
	if java {
		t.Error("Kotlin pom.xml must not also detect Java")
	}
}

func TestDetectProjectType_Java_Pom(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "pom.xml"), []byte(`<project></project>`), 0644)
	_, _, kt, java := DetectProjectType(dir)
	if kt {
		t.Error("plain pom.xml without kotlin should not detect Kotlin")
	}
	if !java {
		t.Error("should detect Java via pom.xml")
	}
}

func TestDetectProjectType_Java_GradleGroovy(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "build.gradle"), []byte(`apply plugin: 'java'`), 0644)
	_, _, kt, java := DetectProjectType(dir)
	if kt {
		t.Error("build.gradle without kotlin should not detect Kotlin")
	}
	if !java {
		t.Error("should detect Java via build.gradle without kotlin")
	}
}

func TestDetectProjectType_Empty(t *testing.T) {
	dir := t.TempDir()
	node, py, kt, java := DetectProjectType(dir)
	if node || py || kt || java {
		t.Errorf("empty dir should detect nothing: node=%v py=%v kt=%v java=%v", node, py, kt, java)
	}
}

func TestDetectProjectType_KotlinTakesPriorityOverJava(t *testing.T) {
	// pom.xml with kotlin keyword: must be Kotlin, not Java
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "pom.xml"), []byte(`<groupId>org.jetbrains.kotlin</groupId>`), 0644)
	_, _, kt, java := DetectProjectType(dir)
	if !kt {
		t.Error("should be Kotlin")
	}
	if java {
		t.Error("should not also be Java when Kotlin detected")
	}
}
