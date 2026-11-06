cmake_minimum_required(VERSION 3.24)
project(probe_spike LANGUAGES CXX)
set(CMAKE_CXX_STANDARD 20)

# Use the already-cloned googletest source; equivalent to FetchContent for
# enumeration purposes and avoids a re-clone.
set(BUILD_GMOCK ON CACHE BOOL "" FORCE)
set(INSTALL_GTEST OFF CACHE BOOL "" FORCE)
add_subdirectory("${GTEST_SRC}" "${CMAKE_BINARY_DIR}/gtest-build" EXCLUDE_FROM_ALL)

# Step 1: what raw targets exist now that CMake has actually run?
set(_raw "")
foreach(_sub "${GTEST_SRC}" "${GTEST_SRC}/googletest" "${GTEST_SRC}/googlemock")
  if(EXISTS "${_sub}/CMakeLists.txt")
    get_property(_t DIRECTORY "${_sub}" PROPERTY BUILDSYSTEM_TARGETS)
    list(APPEND _raw ${_t})
  endif()
endforeach()
message(STATUS "SPIKE_RAW=${_raw}")

# Step 2: namespace candidates harvested statically from the source
#         (set(cmake_package_name GTest ...) is a literal in googletest/CMakeLists.txt)
set(_namespaces GTest gtest googletest)

# Step 3: cross product of namespace x raw name, plus prefix-stripped variants,
#         then verify each with if(TARGET) - the one thing CMake WILL answer.
set(_hits "")
foreach(_ns IN LISTS _namespaces)
  foreach(_r IN LISTS _raw)
    set(_bare "${_r}")
    # strip a "<ns>_" prefix, which is how abseil names absl_base -> absl::base
    string(TOLOWER "${_ns}" _lns)
    if(_bare MATCHES "^${_lns}_(.+)$")
      set(_stripped "${CMAKE_MATCH_1}")
    else()
      set(_stripped "${_bare}")
    endif()
    foreach(_cand "${_ns}::${_bare}" "${_ns}::${_stripped}")
      if(TARGET ${_cand})
        list(APPEND _hits "${_cand}")
      endif()
    endforeach()
  endforeach()
endforeach()
list(REMOVE_DUPLICATES _hits)
message(STATUS "SPIKE_VERIFIED_ALIASES=${_hits}")
